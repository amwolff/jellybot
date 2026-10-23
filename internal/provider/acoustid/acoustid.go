// Package acoustid looks up Chromaprint fingerprints with the AcoustID web
// service.
//
// AcoustID data is licensed under CC BY-SA 3.0.
package acoustid

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/amwolff/jellybot/internal/provider"
)

// DefaultBaseURL is the API root.
const DefaultBaseURL = "https://api.acoustid.org/v2"

// RequestsPerSecond is the AcoustID limit.
const RequestsPerSecond = 3

// Result is one AcoustID match.
type Result struct {
	ID         string
	Score      float64
	Recordings []string
}

// Client is an AcoustID client.
type Client struct {
	p       *provider.Client
	apiKey  string
	BaseURL string
}

// New returns a client that authenticates with the application apiKey.
func New(p *provider.Client, apiKey string) *Client {
	return &Client{p: p, apiKey: apiKey, BaseURL: DefaultBaseURL}
}

type lookupResponse struct {
	Status string `json:"status"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
	Results []struct {
		ID         string  `json:"id"`
		Score      float64 `json:"score"`
		Recordings []struct {
			ID string `json:"id"`
		} `json:"recordings"`
	} `json:"results"`
}

// Lookup returns the matches for a fingerprint of an audio file with the
// given duration in seconds, best match first.
func (c *Client) Lookup(ctx context.Context, fingerprint string, duration int) ([]Result, error) {
	sum := sha256.Sum256([]byte(fingerprint))
	form := url.Values{
		"client":      {c.apiKey},
		"duration":    {strconv.Itoa(duration)},
		"fingerprint": {fingerprint},
		"format":      {"json"},
		"meta":        {"recordingids"},
	}
	resp, err := c.p.Do(ctx, provider.Request{
		Key:    fmt.Sprintf("acoustid:lookup:%s:%d", hex.EncodeToString(sum[:]), duration),
		Policy: provider.NotFoundPolicy,
		New: func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/lookup", strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req, nil
		},
		Empty: func(body []byte) bool {
			var lr lookupResponse
			return json.Unmarshal(body, &lr) == nil && lr.Status == "ok" && len(lr.Results) == 0
		},
	})
	if err != nil {
		return nil, fmt.Errorf("acoustid: %w", err)
	}
	if resp.NotFound {
		return nil, nil
	}

	var lr lookupResponse
	if err := json.Unmarshal(resp.Body, &lr); err != nil {
		return nil, fmt.Errorf("acoustid: %w", err)
	}
	if lr.Status != "ok" {
		return nil, fmt.Errorf("acoustid: %w", errors.New(lr.Error.Message))
	}

	out := make([]Result, 0, len(lr.Results))
	for _, r := range lr.Results {
		res := Result{ID: r.ID, Score: r.Score}
		for _, rec := range r.Recordings {
			res.Recordings = append(res.Recordings, rec.ID)
		}
		out = append(out, res)
	}
	// The service returns the results by score, but do not depend on it.
	slices.SortStableFunc(out, func(a, b Result) int { return cmp.Compare(b.Score, a.Score) })
	return out, nil
}
