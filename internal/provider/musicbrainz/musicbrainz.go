// Package musicbrainz is a small client for the MusicBrainz web service v2.
//
// MusicBrainz core data is licensed under CC0.
package musicbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/amwolff/jellybot/internal/provider"
)

// DefaultBaseURL is the API root.
const DefaultBaseURL = "https://musicbrainz.org/ws/2"

// RequestsPerSecond is the MusicBrainz limit.
const RequestsPerSecond = 1

// Track is a track on a medium.
type Track struct {
	Number   string
	Position int
	Title    string
}

// Medium is a medium (for example, a disc) of a release.
type Medium struct {
	Position   int
	TrackCount int
	// Tracks holds only the tracks of the looked-up recording.
	Tracks []Track
}

// Release is a release that contains a recording.
type Release struct {
	ID             string
	Title          string
	Status         string
	Date           string
	Artist         string
	PrimaryType    string
	SecondaryTypes []string
	Media          []Medium
}

// Recording is a MusicBrainz recording with the releases that contain it.
type Recording struct {
	ID       string
	Title    string
	Artist   string
	Releases []Release
}

// Client is a MusicBrainz client.
type Client struct {
	p       *provider.Client
	BaseURL string
}

// New returns a client.
func New(p *provider.Client) *Client {
	return &Client{p: p, BaseURL: DefaultBaseURL}
}

type artistCredit []struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
}

func (ac artistCredit) String() string {
	var b strings.Builder
	for _, c := range ac {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return b.String()
}

type recordingResponse struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	ArtistCredit artistCredit `json:"artist-credit"`
	Releases     []struct {
		ID           string       `json:"id"`
		Title        string       `json:"title"`
		Status       string       `json:"status"`
		Date         string       `json:"date"`
		ArtistCredit artistCredit `json:"artist-credit"`
		ReleaseGroup struct {
			PrimaryType    string   `json:"primary-type"`
			SecondaryTypes []string `json:"secondary-types"`
		} `json:"release-group"`
		Media []struct {
			Position   int `json:"position"`
			TrackCount int `json:"track-count"`
			Tracks     []struct {
				Number   string `json:"number"`
				Position int    `json:"position"`
				Title    string `json:"title"`
			} `json:"tracks"`
		} `json:"media"`
	} `json:"releases"`
}

func (c *Client) get(ctx context.Context, key, path string, inc string, v any) (bool, error) {
	q := url.Values{"fmt": {"json"}}
	if inc != "" {
		q.Set("inc", inc)
	}
	resp, err := c.p.Do(ctx, provider.Request{
		Key:    "musicbrainz:" + key,
		Policy: provider.NotFoundPolicy,
		New: func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path+"?"+q.Encode(), nil)
		},
	})
	if err != nil {
		return false, fmt.Errorf("musicbrainz: %w", err)
	}
	if resp.NotFound {
		return false, nil
	}
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return false, fmt.Errorf("musicbrainz: %w", err)
	}
	return true, nil
}

// Recording returns the recording with MBID id.
func (c *Client) Recording(ctx context.Context, id string) (Recording, bool, error) {
	var rr recordingResponse
	ok, err := c.get(ctx, "recording:"+id, "/recording/"+url.PathEscape(id), "releases release-groups media artist-credits", &rr)
	if err != nil || !ok {
		return Recording{}, ok, err
	}

	rec := Recording{ID: rr.ID, Title: rr.Title, Artist: rr.ArtistCredit.String()}
	for _, r := range rr.Releases {
		rel := Release{
			ID:             r.ID,
			Title:          r.Title,
			Status:         r.Status,
			Date:           r.Date,
			Artist:         r.ArtistCredit.String(),
			PrimaryType:    r.ReleaseGroup.PrimaryType,
			SecondaryTypes: r.ReleaseGroup.SecondaryTypes,
		}
		for _, m := range r.Media {
			med := Medium{Position: m.Position, TrackCount: m.TrackCount}
			for _, t := range m.Tracks {
				med.Tracks = append(med.Tracks, Track{Number: t.Number, Position: t.Position, Title: t.Title})
			}
			rel.Media = append(rel.Media, med)
		}
		rec.Releases = append(rec.Releases, rel)
	}
	return rec, true, nil
}

type releaseResponse struct {
	Media []struct {
		Position int `json:"position"`
	} `json:"media"`
}

// MediumCount returns the number of media (discs) of the release with MBID
// id.
func (c *Client) MediumCount(ctx context.Context, id string) (int, error) {
	var rr releaseResponse
	ok, err := c.get(ctx, "release:"+id, "/release/"+url.PathEscape(id), "media", &rr)
	if err != nil || !ok {
		return 0, err
	}
	return len(rr.Media), nil
}
