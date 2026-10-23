// Package opensubtitles looks up files by their OpenSubtitles hash with the
// OpenSubtitles REST API.
package opensubtitles

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/amwolff/jellybot/internal/oshash"
	"github.com/amwolff/jellybot/internal/provider"
)

// DefaultBaseURL is the API root.
const DefaultBaseURL = "https://api.opensubtitles.com/api/v1"

// RequestsPerSecond respects the limit of 40 requests in 10 seconds.
const RequestsPerSecond = 4

// userAgent follows the "<<APP_NAME vVERSION>>" format of OpenSubtitles.
const userAgent = "jellybot v" + provider.Version

// Feature types.
const (
	TypeMovie   = "Movie"
	TypeEpisode = "Episode"
)

// Feature is a movie or an episode that matched a hash.
type Feature struct {
	ID           int
	Type         string
	Title        string
	Year         int
	IMDbID       int
	TMDBID       int
	ParentTitle  string
	ParentIMDbID int
	ParentTMDBID int
	Season       int
	Episode      int
}

// Client is an OpenSubtitles client.
type Client struct {
	p       *provider.Client
	apiKey  string
	BaseURL string
}

// New returns a client that authenticates with apiKey.
func New(p *provider.Client, apiKey string) *Client {
	return &Client{p: p, apiKey: apiKey, BaseURL: DefaultBaseURL}
}

type searchResponse struct {
	Data []struct {
		Attributes struct {
			MoviehashMatch bool `json:"moviehash_match"`
			FeatureDetails struct {
				FeatureID     provider.Int    `json:"feature_id"`
				FeatureType   provider.String `json:"feature_type"`
				Year          provider.Int    `json:"year"`
				Title         provider.String `json:"title"`
				MovieName     provider.String `json:"movie_name"`
				IMDbID        provider.Int    `json:"imdb_id"`
				TMDBID        provider.Int    `json:"tmdb_id"`
				SeasonNumber  provider.Int    `json:"season_number"`
				EpisodeNumber provider.Int    `json:"episode_number"`
				ParentIMDbID  provider.Int    `json:"parent_imdb_id"`
				ParentTitle   provider.String `json:"parent_title"`
				ParentTMDBID  provider.Int    `json:"parent_tmdb_id"`
			} `json:"feature_details"`
		} `json:"attributes"`
	} `json:"data"`
}

// SearchHash returns the distinct features whose subtitles OpenSubtitles
// associates with hash h.
func (c *Client) SearchHash(ctx context.Context, h oshash.Hash) ([]Feature, error) {
	// OpenSubtitles redirects unless the parameters are sorted and lower
	// case; url.Values.Encode sorts them.
	q := url.Values{
		"moviehash":       {h.String()},
		"moviehash_match": {"only"},
	}
	resp, err := c.p.Do(ctx, provider.Request{
		Key:    "opensubtitles:moviehash:" + h.String(),
		Policy: provider.NotFoundPolicy,
		New: func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/subtitles?"+q.Encode(), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Api-Key", c.apiKey)
			req.Header.Set("User-Agent", userAgent)
			return req, nil
		},
		Empty: func(body []byte) bool {
			features, err := parse(body)
			return err == nil && len(features) == 0
		},
	})
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: %w", err)
	}
	if resp.NotFound {
		return nil, nil
	}
	features, err := parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: %w", err)
	}
	return features, nil
}

func parse(body []byte) ([]Feature, error) {
	var sr searchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, err
	}

	var out []Feature
	seen := make(map[Feature]bool)
	for _, d := range sr.Data {
		if !d.Attributes.MoviehashMatch {
			continue
		}
		fd := d.Attributes.FeatureDetails
		title := string(fd.Title)
		if title == "" {
			title = string(fd.MovieName)
		}
		f := Feature{
			ID:           int(fd.FeatureID),
			Type:         string(fd.FeatureType),
			Title:        title,
			Year:         int(fd.Year),
			IMDbID:       int(fd.IMDbID),
			TMDBID:       int(fd.TMDBID),
			ParentTitle:  string(fd.ParentTitle),
			ParentIMDbID: int(fd.ParentIMDbID),
			ParentTMDBID: int(fd.ParentTMDBID),
			Season:       int(fd.SeasonNumber),
			Episode:      int(fd.EpisodeNumber),
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out, nil
}

// IMDbTag formats an IMDb number as "tt0133093".
func IMDbTag(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("tt%07d", n)
}
