// Package providertest has helpers for provider client tests.
package providertest

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/amwolff/jellybot/internal/provider"
	"github.com/amwolff/jellybot/internal/store"
)

// Store opens a store in a temporary directory.
func Store(t testing.TB) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "jellybot.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Client returns a provider client without rate limits and with short
// retry delays, backed by a temporary store.
func Client(t testing.TB) *provider.Client {
	t.Helper()
	c := provider.New(Store(t), 1, nil)
	c.Limiter = rate.NewLimiter(rate.Inf, 1)
	c.Backoff = time.Millisecond
	return c
}

// Server starts an HTTP test server.
func Server(t testing.TB, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}
