package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func open(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s, err := Open(filepath.Join(t.TempDir(), "sub", "jellybot.db"), WithClock(c.Now))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, c
}

func TestStat(t *testing.T) {
	s, _ := open(t)

	if _, ok, err := s.Stat("/a"); err != nil || ok {
		t.Fatalf("Stat on empty = %v, %v", ok, err)
	}

	crc := uint32(0xDEADBEEF)
	want := StatEntry{Size: 1, MtimeNs: 2, OSHash: 3, CRC32: &crc}
	if err := s.PutStat("/a", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Stat("/a")
	if err != nil || !ok {
		t.Fatalf("Stat = %v, %v", ok, err)
	}
	if got.Size != want.Size || got.MtimeNs != want.MtimeNs || got.OSHash != want.OSHash || got.CRC32 == nil || *got.CRC32 != crc {
		t.Fatalf("Stat = %+v, want %+v", got, want)
	}
}

func TestIdent(t *testing.T) {
	s, c := open(t)
	k := IdentKey{Size: 1 << 33, OSHash: 0x8e245d9679d31e12}

	if err := s.PutIdent(k, IdentRecord{Kind: "movie", TMDBID: 603, Source: "opensubtitles"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Ident(k)
	if err != nil || !ok {
		t.Fatalf("Ident = %v, %v", ok, err)
	}
	if got.TMDBID != 603 || got.Kind != "movie" || !got.ChosenAt.Equal(c.Now()) {
		t.Fatalf("Ident = %+v", got)
	}

	if _, ok, _ := s.Ident(IdentKey{Size: 1 << 33, OSHash: 1}); ok {
		t.Fatal("unexpected entry for a different hash")
	}

	if err := s.DeleteIdent(k); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Ident(k); ok {
		t.Fatal("entry still present after DeleteIdent")
	}
}

func TestFetchCaches(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()

	var calls int
	fetch := func(context.Context) (Response, error) {
		calls++
		return Response{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}

	r, hit, err := s.Fetch(ctx, "p:k", Policy{}, fetch)
	if err != nil || hit || string(r.Body) != `{"ok":true}` {
		t.Fatalf("first Fetch = %+v, %v, %v", r, hit, err)
	}
	r, hit, err = s.Fetch(ctx, "p:k", Policy{}, fetch)
	if err != nil || !hit || r.Status != 200 || string(r.Body) != `{"ok":true}` {
		t.Fatalf("second Fetch = %+v, %v, %v", r, hit, err)
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times, want 1", calls)
	}

	// A refresh context ignores the cache.
	if _, hit, _ := s.Fetch(WithRefresh(ctx), "p:k", Policy{}, fetch); hit || calls != 2 {
		t.Fatalf("refresh: hit = %v, calls = %d", hit, calls)
	}
}

func TestFetchErrorsAreNotCached(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	boom := errors.New("boom")

	if _, _, err := s.Fetch(ctx, "p:k", Policy{}, func(context.Context) (Response, error) {
		return Response{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, hit, err := s.Fetch(ctx, "p:k", Policy{}, func(context.Context) (Response, error) {
		return Response{Status: 200}, nil
	}); err != nil || hit {
		t.Fatalf("hit = %v, err = %v", hit, err)
	}
}

func TestFetchExpiry(t *testing.T) {
	const day = 24 * time.Hour
	tmdb := Policy{MaxAge: 180 * day, NotFoundMaxAge: 30 * day}
	forever := Policy{NotFoundMaxAge: 30 * day}

	tests := []struct {
		name     string
		policy   Policy
		notFound bool
		age      time.Duration
		wantHit  bool
	}{
		{"not found, fresh", forever, true, 29 * day, true},
		{"not found, 31 days", forever, true, 31 * day, false},
		{"found, 31 days", forever, false, 31 * day, true},
		{"found, 10 years", forever, false, 3650 * day, true},
		{"tmdb, 179 days", tmdb, false, 179 * day, true},
		{"tmdb, 181 days", tmdb, false, 181 * day, false},
		{"tmdb not found, 31 days", tmdb, true, 31 * day, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, c := open(t)
			ctx := context.Background()
			calls := 0
			fetch := func(context.Context) (Response, error) {
				calls++
				return Response{Status: 200, NotFound: tt.notFound}, nil
			}
			if _, _, err := s.Fetch(ctx, "k", tt.policy, fetch); err != nil {
				t.Fatal(err)
			}
			c.Advance(tt.age)
			_, hit, err := s.Fetch(ctx, "k", tt.policy, fetch)
			if err != nil {
				t.Fatal(err)
			}
			if hit != tt.wantHit {
				t.Fatalf("hit = %v, want %v", hit, tt.wantHit)
			}
			if wantCalls := map[bool]int{true: 1, false: 2}[tt.wantHit]; calls != wantCalls {
				t.Fatalf("calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestFetchSharesConcurrentCalls(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()

	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func(context.Context) (Response, error) {
		calls.Add(1)
		<-release
		return Response{Status: 200}, nil
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, _, err := s.Fetch(ctx, "k", Policy{}, fetch); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := calls.Load(); n > 2 {
		// Late arrivals may miss the shared call but then hit the cache;
		// at most one extra fetch can slip in between.
		t.Fatalf("fetch called %d times", n)
	}
}

func TestOpenInUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jellybot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if _, err := Open(path); !errors.Is(err, ErrInUse) {
		t.Fatalf("second Open err = %v, want ErrInUse", err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jellybot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PutStat("/x", StatEntry{Size: 7}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if e, ok, err := s.Stat("/x"); err != nil || !ok || e.Size != 7 {
		t.Fatalf("Stat after reopen = %+v, %v, %v", e, ok, err)
	}
}
