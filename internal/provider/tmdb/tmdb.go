// Package tmdb is a small client for the TMDB API v3.
//
// This product uses the TMDB API but is not endorsed or certified by TMDB.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/amwolff/jellybot/internal/provider"
	"github.com/amwolff/jellybot/internal/store"
)

// DefaultBaseURL is the API root.
const DefaultBaseURL = "https://api.themoviedb.org/3"

// RequestsPerSecond stays well below the soft TMDB limit.
const RequestsPerSecond = 20

// Policy follows the TMDB terms, which do not permit cached data older than
// six months.
var Policy = store.Policy{MaxAge: 180 * provider.Day, NotFoundMaxAge: 30 * provider.Day}

// Movie is a TMDB movie.
type Movie struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	IMDbID      string `json:"imdb_id"`
}

// Year returns the release year, or 0.
func (m Movie) Year() int { return year(m.ReleaseDate) }

// Series is a TMDB TV series.
type Series struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	FirstAirDate string `json:"first_air_date"`
}

// Year returns the first-air year, or 0.
func (s Series) Year() int { return year(s.FirstAirDate) }

// Episode is a TMDB TV episode.
type Episode struct {
	ID            int    `json:"id"`
	ShowID        int    `json:"show_id"`
	Name          string `json:"name"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	AirDate       string `json:"air_date"`
}

// FindResult is the result of a lookup by external ID.
type FindResult struct {
	Movies   []Movie   `json:"movie_results"`
	Series   []Series  `json:"tv_results"`
	Episodes []Episode `json:"tv_episode_results"`
}

func (r FindResult) empty() bool {
	return len(r.Movies) == 0 && len(r.Series) == 0 && len(r.Episodes) == 0
}

// Client is a TMDB client.
type Client struct {
	p       *provider.Client
	key     string
	BaseURL string
}

// New returns a client. key is either an API key (v3) or an API read access
// token (a JWT).
func New(p *provider.Client, key string) *Client {
	return &Client{p: p, key: key, BaseURL: DefaultBaseURL}
}

func (c *Client) get(ctx context.Context, cacheKey, path string, q url.Values, empty func([]byte) bool, v any) (bool, error) {
	resp, err := c.p.Do(ctx, provider.Request{
		Key:    "tmdb:" + cacheKey,
		Policy: Policy,
		New: func(ctx context.Context) (*http.Request, error) {
			params := cloneValues(q)
			bearer := strings.Count(c.key, ".") == 2
			if !bearer {
				params.Set("api_key", c.key)
			}
			u := c.BaseURL + path
			if len(params) > 0 {
				u += "?" + params.Encode()
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			if bearer {
				req.Header.Set("Authorization", "Bearer "+c.key)
			}
			return req, nil
		},
		Empty: empty,
	})
	if err != nil {
		return false, fmt.Errorf("tmdb: %w", err)
	}
	if resp.NotFound {
		return false, nil
	}
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return false, fmt.Errorf("tmdb: %w", err)
	}
	return true, nil
}

// FindIMDb looks up an IMDb ID ("tt…").
func (c *Client) FindIMDb(ctx context.Context, imdbID string) (FindResult, error) {
	var r FindResult
	_, err := c.get(ctx, "find:"+imdbID, "/find/"+url.PathEscape(imdbID),
		url.Values{"external_source": {"imdb_id"}},
		func(body []byte) bool {
			var fr FindResult
			return json.Unmarshal(body, &fr) == nil && fr.empty()
		}, &r)
	return r, err
}

// Movie returns the movie with id.
func (c *Client) Movie(ctx context.Context, id int) (Movie, bool, error) {
	var m Movie
	ok, err := c.get(ctx, "movie:"+strconv.Itoa(id), "/movie/"+strconv.Itoa(id), nil, nil, &m)
	return m, ok, err
}

// Series returns the TV series with id.
func (c *Client) Series(ctx context.Context, id int) (Series, bool, error) {
	var s Series
	ok, err := c.get(ctx, "tv:"+strconv.Itoa(id), "/tv/"+strconv.Itoa(id), nil, nil, &s)
	return s, ok, err
}

// Episode returns an episode of the TV series with seriesID.
func (c *Client) Episode(ctx context.Context, seriesID, season, episode int) (Episode, bool, error) {
	var e Episode
	key := fmt.Sprintf("tv:%d:s%d:e%d", seriesID, season, episode)
	path := fmt.Sprintf("/tv/%d/season/%d/episode/%d", seriesID, season, episode)
	ok, err := c.get(ctx, key, path, nil, nil, &e)
	return e, ok, err
}

func cloneValues(q url.Values) url.Values {
	out := make(url.Values, len(q)+1)
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
