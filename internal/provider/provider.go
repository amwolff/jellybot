// Package provider holds the HTTP plumbing that the metadata provider
// clients share: the cache, rate limits, and retries.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/amwolff/jellybot/internal/store"
)

// Version is the jellybot version that the clients announce.
const Version = "0.1.0"

// UserAgent identifies jellybot with contact data, which MusicBrainz
// requires.
const UserAgent = "jellybot/" + Version + " ( https://github.com/amwolff/jellybot )"

// Day is a convenience duration for cache policies.
const Day = 24 * time.Hour

// NotFoundPolicy expires "not found" responses after 30 days and keeps the
// others forever.
var NotFoundPolicy = store.Policy{NotFoundMaxAge: 30 * Day}

const maxBody = 16 << 20

// Client performs cached, rate-limited HTTP requests.
type Client struct {
	HTTP    *http.Client
	Store   *store.Store
	Limiter *rate.Limiter
	Log     *slog.Logger
	// MaxRetries is the number of retries after 429 and 5xx responses.
	MaxRetries int
	// Backoff is the first retry delay. It doubles after each retry.
	Backoff time.Duration
}

// New returns a Client that sends at most perSecond requests each second.
func New(s *store.Store, perSecond float64, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Store:      s,
		Limiter:    rate.NewLimiter(rate.Limit(perSecond), 1),
		Log:        log,
		MaxRetries: 4,
		Backoff:    time.Second,
	}
}

// Request describes one cached request.
type Request struct {
	// Key is the cache key. It must not contain secrets.
	Key    string
	Policy store.Policy
	// New builds the HTTP request. Do can call it more than once.
	New func(ctx context.Context) (*http.Request, error)
	// Empty reports whether a successful response holds no result. Status
	// 404 always counts as "not found".
	Empty func(body []byte) bool
}

// StatusError is returned for unexpected HTTP statuses.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("provider: HTTP %d: %s", e.Status, e.Body)
}

// Do returns the response for r, from the cache if possible.
func (c *Client) Do(ctx context.Context, r Request) (store.Response, error) {
	resp, hit, err := c.Store.Fetch(ctx, r.Key, r.Policy, func(ctx context.Context) (store.Response, error) {
		return c.fetch(ctx, r)
	})
	if err != nil {
		return store.Response{}, err
	}
	c.Log.DebugContext(ctx, "provider response", "key", r.Key, "cached", hit, "status", resp.Status, "notFound", resp.NotFound)
	return resp, nil
}

func (c *Client) fetch(ctx context.Context, r Request) (store.Response, error) {
	backoff := c.Backoff
	for attempt := 0; ; attempt++ {
		if err := c.Limiter.Wait(ctx); err != nil {
			return store.Response{}, err
		}

		req, err := r.New(ctx)
		if err != nil {
			return store.Response{}, err
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", UserAgent)
		}
		if req.Header.Get("Accept") == "" {
			req.Header.Set("Accept", "application/json")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return store.Response{}, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		if err != nil {
			return store.Response{}, err
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			empty := r.Empty != nil && r.Empty(body)
			return store.Response{Status: resp.StatusCode, NotFound: empty, Body: body}, nil
		case resp.StatusCode == http.StatusNotFound:
			return store.Response{Status: resp.StatusCode, NotFound: true, Body: body}, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			if attempt >= c.MaxRetries {
				return store.Response{}, &StatusError{Status: resp.StatusCode, Body: snippet(body)}
			}
			wait := backoff
			if d, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
				wait = d
			}
			c.Log.DebugContext(ctx, "provider retry", "key", r.Key, "status", resp.StatusCode, "wait", wait)
			select {
			case <-ctx.Done():
				return store.Response{}, ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
		default:
			return store.Response{}, &StatusError{Status: resp.StatusCode, Body: snippet(body)}
		}
	}
}

func retryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return min(time.Duration(secs)*time.Second, time.Minute), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(t), 0), time.Minute), true
	}
	return 0, false
}

func snippet(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) > 200 {
		b = append(b[:200:200], "…"...)
	}
	return string(b)
}

// Int is an integer that decodes from a JSON number, a numeric string, or
// null. Some providers are not consistent about the types of IDs.
type Int int

// UnmarshalJSON implements json.Unmarshaler.
func (i *Int) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		*i = 0
		return nil
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(string(b), 64)
		if ferr != nil {
			return errors.Join(err, ferr)
		}
		n = int64(f)
	}
	*i = Int(n)
	return nil
}

// String is a string that decodes from a JSON string, a number, or null.
type String string

// UnmarshalJSON implements json.Unmarshaler.
func (s *String) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	if len(b) >= 2 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = String(v)
		return nil
	}
	*s = String(b)
	return nil
}
