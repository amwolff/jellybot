// Package store is jellybot's embedded database. It records file hashes,
// identities, and raw provider responses in a single bbolt file.
package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
	"golang.org/x/sync/singleflight"
)

// ErrInUse is returned when another process holds the database.
var ErrInUse = errors.New("store: database in use by another jellybot process")

const schemaVersion = 1

var (
	bucketMeta  = []byte("meta")
	bucketStat  = []byte("stat")
	bucketIdent = []byte("ident")
	bucketHTTP  = []byte("http")

	keySchema = []byte("schema")
)

// Store is the jellybot database.
type Store struct {
	db    *bbolt.DB
	now   func() time.Time
	group singleflight.Group
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock that the store uses to determine entry ages.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// DefaultPath returns the default database path.
func DefaultPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "jellybot", "jellybot.db")
}

// Open opens (or creates) the database at path.
func Open(path string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		if errors.Is(err, berrors.ErrTimeout) {
			return nil, ErrInUse
		}
		return nil, fmt.Errorf("store: %w", err)
	}

	s := &Store{db: db, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}

	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketStat, bucketIdent, bucketHTTP} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(bucketMeta)
		if v := meta.Get(keySchema); v != nil {
			if got := binary.BigEndian.Uint32(v); got != schemaVersion {
				return fmt.Errorf("unsupported schema version %d (want %d)", got, schemaVersion)
			}
			return nil
		}
		return meta.Put(keySchema, binary.BigEndian.AppendUint32(nil, schemaVersion))
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}

	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) get(bucket, key []byte, v any) (bool, error) {
	var data []byte
	if err := s.db.View(func(tx *bbolt.Tx) error {
		if b := tx.Bucket(bucket).Get(key); b != nil {
			data = append([]byte(nil), b...)
		}
		return nil
	}); err != nil {
		return false, err
	}
	if data == nil {
		return false, nil
	}
	return true, json.Unmarshal(data, v)
}

func (s *Store) put(bucket, key []byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.db.Batch(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucket).Put(key, data)
	})
}

func (s *Store) delete(bucket, key []byte) error {
	return s.db.Batch(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucket).Delete(key)
	})
}

// StatEntry memoizes the hashes of a file at a path. It is valid only while
// the size and the modification time of the file stay the same.
type StatEntry struct {
	Size    int64   `json:"size"`
	MtimeNs int64   `json:"mtimeNs"`
	OSHash  uint64  `json:"oshash"`
	CRC32   *uint32 `json:"crc32,omitempty"`
}

// Stat returns the memo for path.
func (s *Store) Stat(path string) (StatEntry, bool, error) {
	var e StatEntry
	ok, err := s.get(bucketStat, []byte(path), &e)
	return e, ok, err
}

// PutStat records the memo for path.
func (s *Store) PutStat(path string, e StatEntry) error {
	return s.put(bucketStat, []byte(path), e)
}

// IdentKey identifies file contents independently of their path.
type IdentKey struct {
	Size   int64
	OSHash uint64
}

func (k IdentKey) bytes() []byte {
	b := binary.BigEndian.AppendUint64(nil, uint64(k.Size)) //nolint:gosec // sizes are not negative
	return binary.BigEndian.AppendUint64(b, k.OSHash)
}

// IdentRecord holds only the IDs of an identified file. Names come from
// the providers (and their cache entries), so that this record never
// holds provider data that can expire.
type IdentRecord struct {
	Kind        string    `json:"kind"`
	TMDBID      int       `json:"tmdbID,omitempty"`
	IMDbID      string    `json:"imdbID,omitempty"`
	Season      int       `json:"season,omitempty"`
	Episode     int       `json:"episode,omitempty"`
	MBRecording string    `json:"mbRecording,omitempty"`
	MBRelease   string    `json:"mbRelease,omitempty"`
	Source      string    `json:"source"`
	ChosenAt    time.Time `json:"chosenAt"`
}

// Ident returns the identity recorded for k.
func (s *Store) Ident(k IdentKey) (IdentRecord, bool, error) {
	var r IdentRecord
	ok, err := s.get(bucketIdent, k.bytes(), &r)
	return r, ok, err
}

// PutIdent records the identity for k.
func (s *Store) PutIdent(k IdentKey, r IdentRecord) error {
	if r.ChosenAt.IsZero() {
		r.ChosenAt = s.now()
	}
	return s.put(bucketIdent, k.bytes(), r)
}

// DeleteIdent removes the identity recorded for k.
func (s *Store) DeleteIdent(k IdentKey) error {
	return s.delete(bucketIdent, k.bytes())
}

// Response is a cached provider response.
type Response struct {
	FetchedAt time.Time
	Status    int
	// NotFound is true when the provider had no result. Providers decide
	// this themselves, because many answer "no result" with status 200.
	NotFound bool
	Body     []byte
}

const responseVersion = 1

func (r Response) marshal() []byte {
	b := make([]byte, 0, 12+len(r.Body))
	b = append(b, responseVersion)
	b = binary.BigEndian.AppendUint64(b, uint64(r.FetchedAt.UnixNano()))     //nolint:gosec // a bit-for-bit round trip
	b = binary.BigEndian.AppendUint16(b, uint16(min(max(r.Status, 0), 999))) //nolint:gosec // HTTP statuses have three digits
	var flags byte
	if r.NotFound {
		flags |= 1
	}
	b = append(b, flags)
	return append(b, r.Body...)
}

func unmarshalResponse(b []byte) (Response, error) {
	if len(b) < 12 || b[0] != responseVersion {
		return Response{}, errors.New("store: malformed cached response")
	}
	return Response{
		FetchedAt: time.Unix(0, int64(binary.BigEndian.Uint64(b[1:9]))), //nolint:gosec // a bit-for-bit round trip
		Status:    int(binary.BigEndian.Uint16(b[9:11])),
		NotFound:  b[11]&1 != 0,
		Body:      append([]byte(nil), b[12:]...),
	}, nil
}

// Policy sets how long cached responses stay valid. A zero duration means
// that the entries never expire.
type Policy struct {
	// MaxAge applies to all responses.
	MaxAge time.Duration
	// NotFoundMaxAge applies to responses with NotFound set.
	NotFoundMaxAge time.Duration
}

func (p Policy) expired(r Response, now time.Time) bool {
	age := now.Sub(r.FetchedAt)
	if p.MaxAge > 0 && age > p.MaxAge {
		return true
	}
	return r.NotFound && p.NotFoundMaxAge > 0 && age > p.NotFoundMaxAge
}

type refreshKey struct{}

// WithRefresh returns a context that makes Fetch ignore cached responses.
func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshKey{}, true)
}

// IsRefresh reports whether ctx came from WithRefresh.
func IsRefresh(ctx context.Context) bool {
	v, _ := ctx.Value(refreshKey{}).(bool)
	return v
}

// Fetch returns the cached response for key. If there is no entry, or the
// entry expired under p, or ctx came from WithRefresh, Fetch discards the
// entry, calls fetch, and records its result. Concurrent calls for the same
// key share one fetch. The boolean result reports a cache hit.
func (s *Store) Fetch(ctx context.Context, key string, p Policy, fetch func(context.Context) (Response, error)) (Response, bool, error) {
	k := []byte(key)

	if !IsRefresh(ctx) {
		var raw []byte
		if err := s.db.View(func(tx *bbolt.Tx) error {
			if b := tx.Bucket(bucketHTTP).Get(k); b != nil {
				raw = append([]byte(nil), b...)
			}
			return nil
		}); err != nil {
			return Response{}, false, err
		}
		if raw != nil {
			r, err := unmarshalResponse(raw)
			if err == nil && !p.expired(r, s.now()) {
				return r, true, nil
			}
			if err := s.delete(bucketHTTP, k); err != nil {
				return Response{}, false, err
			}
		}
	}

	v, err, _ := s.group.Do(key, func() (any, error) {
		r, err := fetch(ctx)
		if err != nil {
			return Response{}, err
		}
		if r.FetchedAt.IsZero() {
			r.FetchedAt = s.now()
		}
		if err := s.db.Batch(func(tx *bbolt.Tx) error {
			return tx.Bucket(bucketHTTP).Put(k, r.marshal())
		}); err != nil {
			return Response{}, err
		}
		return r, nil
	})
	if err != nil {
		return Response{}, false, err
	}
	return v.(Response), false, nil
}
