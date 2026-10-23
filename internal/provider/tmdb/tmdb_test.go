package tmdb

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/amwolff/jellybot/internal/provider/providertest"
)

func server(t *testing.T, wantAuth func(*http.Request) bool) string {
	t.Helper()
	srv := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if !wantAuth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/find/tt0133093":
			if r.URL.Query().Get("external_source") != "imdb_id" {
				t.Errorf("external_source = %q", r.URL.Query().Get("external_source"))
			}
			_, _ = w.Write([]byte(`{"movie_results":[{"id":603,"title":"The Matrix","release_date":"1999-03-30"}],"tv_results":[],"tv_episode_results":[]}`))
		case "/find/tt0959621":
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[],"tv_episode_results":[{"id":62085,"name":"Pilot","show_id":1396,"season_number":1,"episode_number":1}]}`))
		case "/find/tt9999999":
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[],"tv_episode_results":[],"person_results":[]}`))
		case "/movie/603":
			_, _ = w.Write([]byte(`{"id":603,"title":"The Matrix","release_date":"1999-03-30","imdb_id":"tt0133093","runtime":136}`))
		case "/tv/1396":
			_, _ = w.Write([]byte(`{"id":1396,"name":"Breaking Bad","first_air_date":"2008-01-20"}`))
		case "/tv/1396/season/1/episode/1":
			_, _ = w.Write([]byte(`{"id":62085,"name":"Pilot","season_number":1,"episode_number":1,"air_date":"2008-01-20"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"status_code":34,"status_message":"The resource you requested could not be found."}`))
		}
	})
	return srv.URL
}

func TestClient(t *testing.T) {
	c := New(providertest.Client(t), "v3key")
	c.BaseURL = server(t, func(r *http.Request) bool { return r.URL.Query().Get("api_key") == "v3key" })
	ctx := context.Background()

	f, err := c.FindIMDb(ctx, "tt0133093")
	if err != nil || len(f.Movies) != 1 || f.Movies[0].ID != 603 {
		t.Fatalf("FindIMDb = %+v, %v", f, err)
	}

	f, err = c.FindIMDb(ctx, "tt0959621")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Episode{{ID: 62085, ShowID: 1396, Name: "Pilot", SeasonNumber: 1, EpisodeNumber: 1}}; !reflect.DeepEqual(f.Episodes, want) {
		t.Fatalf("FindIMDb episodes = %+v", f.Episodes)
	}

	f, err = c.FindIMDb(ctx, "tt9999999")
	if err != nil || !f.empty() {
		t.Fatalf("FindIMDb(unknown) = %+v, %v", f, err)
	}

	m, ok, err := c.Movie(ctx, 603)
	if err != nil || !ok || m.Title != "The Matrix" || m.Year() != 1999 || m.IMDbID != "tt0133093" {
		t.Fatalf("Movie = %+v, %v, %v", m, ok, err)
	}

	s, ok, err := c.Series(ctx, 1396)
	if err != nil || !ok || s.Name != "Breaking Bad" || s.Year() != 2008 {
		t.Fatalf("Series = %+v, %v, %v", s, ok, err)
	}

	e, ok, err := c.Episode(ctx, 1396, 1, 1)
	if err != nil || !ok || e.Name != "Pilot" {
		t.Fatalf("Episode = %+v, %v, %v", e, ok, err)
	}

	_, ok, err = c.Movie(ctx, 1)
	if err != nil || ok {
		t.Fatalf("Movie(unknown) = %v, %v", ok, err)
	}
}

func TestBearerToken(t *testing.T) {
	// A fake JWT; only its shape (three dot-separated parts) matters.
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ4In0.c2lnbmF0dXJl"
	c := New(providertest.Client(t), jwt)
	c.BaseURL = server(t, func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+jwt && r.URL.Query().Get("api_key") == ""
	})

	if _, ok, err := c.Movie(context.Background(), 603); err != nil || !ok {
		t.Fatalf("Movie = %v, %v", ok, err)
	}
}
