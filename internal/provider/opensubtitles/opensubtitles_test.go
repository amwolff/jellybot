package opensubtitles

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/amwolff/jellybot/internal/oshash"
	"github.com/amwolff/jellybot/internal/provider/providertest"
)

// The shape follows the example in the OpenSubtitles OpenAPI spec.
const fixture = `{
  "total_pages": 1, "total_count": 4, "per_page": 60, "page": 1,
  "data": [
    {"id": "1", "type": "subtitle", "attributes": {"moviehash_match": true, "feature_details": {
      "feature_id": 805, "feature_type": "Episode", "year": 2004, "title": "Come In, Stranger",
      "movie_name": "Desperate Housewives - S01E05 Come In, Stranger",
      "imdb_id": 558705, "tmdb_id": 38366, "season_number": 1, "episode_number": 5,
      "parent_imdb_id": 410975, "parent_title": "Desperate Housewives", "parent_tmdb_id": 693,
      "parent_feature_id": 107}}},
    {"id": "2", "type": "subtitle", "attributes": {"moviehash_match": true, "feature_details": {
      "feature_id": 805, "feature_type": "Episode", "year": 2004, "title": "Come In, Stranger",
      "imdb_id": 558705, "tmdb_id": 38366, "season_number": 1, "episode_number": 5,
      "parent_imdb_id": 410975, "parent_title": "Desperate Housewives", "parent_tmdb_id": 693}}},
    {"id": "3", "type": "subtitle", "attributes": {"moviehash_match": false, "feature_details": {
      "feature_id": 9, "feature_type": "Movie", "year": 1999, "title": "Unrelated", "imdb_id": 1, "tmdb_id": 2}}},
    {"id": "4", "type": "subtitle", "attributes": {"moviehash_match": true, "feature_details": {
      "feature_id": 10, "feature_type": "Movie", "year": 1999, "title": "The Matrix",
      "imdb_id": "133093", "tmdb_id": 603, "season_number": null}}}
  ]
}`

func TestSearchHash(t *testing.T) {
	var calls int
	srv := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/subtitles" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "moviehash=8e245d9679d31e12&moviehash_match=only" {
			t.Errorf("query = %q", got)
		}
		if r.Header.Get("Api-Key") != "secret" {
			t.Errorf("Api-Key = %q", r.Header.Get("Api-Key"))
		}
		if r.Header.Get("User-Agent") != "jellybot v0.1.0" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(fixture))
	})
	c := New(providertest.Client(t), "secret")
	c.BaseURL = srv.URL

	got, err := c.SearchHash(context.Background(), oshash.Hash(0x8e245d9679d31e12))
	if err != nil {
		t.Fatal(err)
	}
	want := []Feature{
		{ID: 805, Type: TypeEpisode, Title: "Come In, Stranger", Year: 2004, IMDbID: 558705, TMDBID: 38366,
			ParentTitle: "Desperate Housewives", ParentIMDbID: 410975, ParentTMDBID: 693, Season: 1, Episode: 5},
		{ID: 10, Type: TypeMovie, Title: "The Matrix", Year: 1999, IMDbID: 133093, TMDBID: 603},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchHash =\n%+v\nwant\n%+v", got, want)
	}

	// The key is not part of the cache key, so a second client with a
	// different key still hits the cache.
	if _, err := c.SearchHash(context.Background(), oshash.Hash(0x8e245d9679d31e12)); err != nil || calls != 1 {
		t.Fatalf("second call: err = %v, calls = %d", err, calls)
	}
}

func TestSearchHashNoMatch(t *testing.T) {
	srv := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_pages":0,"total_count":0,"data":[]}`))
	})
	c := New(providertest.Client(t), "secret")
	c.BaseURL = srv.URL

	got, err := c.SearchHash(context.Background(), 1)
	if err != nil || len(got) != 0 {
		t.Fatalf("SearchHash = %v, %v", got, err)
	}
}

func TestIMDbTag(t *testing.T) {
	for in, want := range map[int]string{133093: "tt0133093", 12801262: "tt12801262", 0: ""} {
		if got := IMDbTag(in); got != want {
			t.Errorf("IMDbTag(%d) = %q, want %q", in, got, want)
		}
	}
}
