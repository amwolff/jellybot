// Package srrdb finds scene releases by the exact size of an archived file,
// and confirms them by the CRC32 of that file.
package srrdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/amwolff/jellybot/internal/provider"
)

// DefaultBaseURL is the API root.
const DefaultBaseURL = "https://api.srrdb.com/v1"

// RequestsPerSecond is polite: the srrDB terms say "use but don't scrape".
const RequestsPerSecond = 1

// Release is a scene release.
type Release struct {
	Name string
	// IMDbID is "tt…", or empty if srrDB does not know it.
	IMDbID string
}

// ArchivedFile is a file inside the RAR archives of a release.
type ArchivedFile struct {
	Name string
	Size int64
	CRC  uint32
}

// Client is an srrDB client.
type Client struct {
	p       *provider.Client
	BaseURL string
}

// New returns a client.
func New(p *provider.Client) *Client {
	return &Client{p: p, BaseURL: DefaultBaseURL}
}

type searchResponse struct {
	Results []struct {
		Release string          `json:"release"`
		IMDbID  provider.String `json:"imdbId"`
	} `json:"results"`
	ResultsCount provider.Int `json:"resultsCount"`
}

// SearchSize returns the distinct releases that contain an archived file of
// exactly size bytes, and the total number of results. The search matches
// any archived file, so small sizes give many unrelated results.
func (c *Client) SearchSize(ctx context.Context, size int64) ([]Release, int, error) {
	n := strconv.FormatInt(size, 10)
	resp, err := c.p.Do(ctx, provider.Request{
		Key:    "srrdb:archive-size:" + n,
		Policy: provider.NotFoundPolicy,
		New: func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/search/archive-size:"+n, nil)
		},
		Empty: func(body []byte) bool {
			var sr searchResponse
			return json.Unmarshal(body, &sr) == nil && len(sr.Results) == 0
		},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("srrdb: %w", err)
	}
	if resp.NotFound {
		return nil, 0, nil
	}

	var sr searchResponse
	if err := json.Unmarshal(resp.Body, &sr); err != nil {
		return nil, 0, fmt.Errorf("srrdb: %w", err)
	}
	var out []Release
	seen := make(map[string]bool)
	for _, r := range sr.Results {
		if r.Release == "" || seen[r.Release] {
			continue
		}
		seen[r.Release] = true
		rel := Release{Name: r.Release}
		if id := strings.TrimPrefix(string(r.IMDbID), "tt"); id != "" {
			rel.IMDbID = "tt" + id
		}
		out = append(out, rel)
	}
	return out, max(int(sr.ResultsCount), len(sr.Results)), nil
}

type detailsResponse struct {
	ArchivedFiles []struct {
		Name string          `json:"name"`
		Size provider.Int    `json:"size"`
		CRC  provider.String `json:"crc"`
	} `json:"archived-files"`
}

// ArchivedFiles returns the archived files of release.
func (c *Client) ArchivedFiles(ctx context.Context, release string) ([]ArchivedFile, error) {
	resp, err := c.p.Do(ctx, provider.Request{
		Key:    "srrdb:details:" + release,
		Policy: provider.NotFoundPolicy,
		New: func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/details/"+url.PathEscape(release), nil)
		},
		// An unknown release gives "[]" instead of an object.
		Empty: func(body []byte) bool {
			return bytes.HasPrefix(bytes.TrimSpace(body), []byte("["))
		},
	})
	if err != nil {
		return nil, fmt.Errorf("srrdb: %w", err)
	}
	if resp.NotFound {
		return nil, nil
	}

	var dr detailsResponse
	if err := json.Unmarshal(resp.Body, &dr); err != nil {
		return nil, fmt.Errorf("srrdb: %w", err)
	}
	var out []ArchivedFile
	for _, f := range dr.ArchivedFiles {
		crc, err := strconv.ParseUint(string(f.CRC), 16, 32)
		if err != nil {
			continue
		}
		out = append(out, ArchivedFile{Name: f.Name, Size: int64(f.Size), CRC: uint32(crc)})
	}
	return out, nil
}
