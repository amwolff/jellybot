package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/amwolff/jellybot/internal/provider"
	"github.com/amwolff/jellybot/internal/provider/providertest"
)

func request(url string) provider.Request {
	return provider.Request{
		Key:    "test:" + url,
		Policy: provider.NotFoundPolicy,
		New: func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		},
		Empty: func(body []byte) bool { return string(body) == "[]" },
	}
}

func TestDoRetriesAndCaches(t *testing.T) {
	var calls atomic.Int32
	srv := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != provider.UserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	c := providertest.Client(t)

	resp, err := c.Do(context.Background(), request(srv.URL))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != 200 || resp.NotFound || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("resp = %+v", resp)
	}
	if _, err := c.Do(context.Background(), request(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("server calls = %d, want 3 (two retries, then the cache)", n)
	}
}

func TestDoNotFound(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"404", http.StatusNotFound, "nope"},
		{"empty 200", http.StatusOK, "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			c := providertest.Client(t)
			for range 2 {
				resp, err := c.Do(context.Background(), request(srv.URL))
				if err != nil {
					t.Fatal(err)
				}
				if !resp.NotFound {
					t.Fatalf("resp = %+v, want NotFound", resp)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestDoErrorsAreNotCached(t *testing.T) {
	var calls atomic.Int32
	srv := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad key"}`))
	})
	c := providertest.Client(t)

	for range 2 {
		_, err := c.Do(context.Background(), request(srv.URL))
		var se *provider.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
			t.Fatalf("err = %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestDoGivesUpAfterRetries(t *testing.T) {
	var calls atomic.Int32
	srv := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := providertest.Client(t)
	c.MaxRetries = 2

	if _, err := c.Do(context.Background(), request(srv.URL)); err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestIntAndString(t *testing.T) {
	var v struct {
		A, B, C, D provider.Int
		E, F, G    provider.String
	}
	in := `{"A": 42, "B": "0123755", "C": null, "D": 1.0, "E": "x", "F": 12, "G": null}`
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 42 || v.B != 123755 || v.C != 0 || v.D != 1 || v.E != "x" || v.F != "12" || v.G != "" {
		t.Fatalf("decoded %+v", v)
	}
	if err := json.Unmarshal([]byte(`{"A": "abc"}`), &v); err == nil {
		t.Fatal("expected an error for a non-numeric Int")
	}
}
