package musicbrainz

import (
	"context"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/amwolff/jellybot/internal/provider"
	"github.com/amwolff/jellybot/internal/provider/providertest"
)

func TestRecording(t *testing.T) {
	recording, err := os.ReadFile("testdata/recording.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != provider.UserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if r.URL.Query().Get("fmt") != "json" {
			t.Errorf("fmt = %q", r.URL.Query().Get("fmt"))
		}
		switch r.URL.Path {
		case "/recording/05cf5b1f-6103-4410-922a-ef41bab995d2":
			if got := r.URL.Query().Get("inc"); got != "releases release-groups media artist-credits" {
				t.Errorf("inc = %q", got)
			}
			_, _ = w.Write(recording)
		case "/release/6b94230e-938a-4e4d-9e4e-c0d9d00365c8":
			_, _ = w.Write([]byte(`{"id":"6b94230e-938a-4e4d-9e4e-c0d9d00365c8","media":[{"position":1},{"position":2}]}`))
		case "/release/b84ee12a-09ef-421b-82de-0441a926375b":
			if got := r.URL.Query().Get("inc"); got != "recordings artist-credits" {
				_, _ = w.Write([]byte(`{"media":[{"position":1}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"b84ee12a-09ef-421b-82de-0441a926375b","title":"The Dark Side of the Moon","status":"Official",
				"date":"1973-03-24","artist-credit":[{"name":"Pink Floyd","joinphrase":""}],
				"media":[{"position":1,"track-count":2,"tracks":[
				 {"id":"d4156411-b884-368f-a4cb-7c0101a557a2","number":"A1","position":1,"title":"Speak to Me","length":68346,
				  "recording":{"id":"bef3fddb-5aca-49f5-b2fd-d56a23268d63","title":"Speak to Me","length":68346}},
				 {"id":"t2","number":"A2","position":2,"title":"Breathe","length":null,"recording":{"id":"r2"}}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Not Found"}`))
		}
	})
	c := New(providertest.Client(t))
	c.BaseURL = srv.URL
	ctx := context.Background()

	rec, ok, err := c.Recording(ctx, "05cf5b1f-6103-4410-922a-ef41bab995d2")
	if err != nil || !ok {
		t.Fatalf("Recording = %v, %v", ok, err)
	}
	if rec.Title != "One More Time" || rec.Artist != "Daft Punk" || len(rec.Releases) != 1 {
		t.Fatalf("Recording = %+v", rec)
	}
	rel := rec.Releases[0]
	if rel.Title != "Glowsticks of Steel" || rel.Status != "Official" || rel.Artist != "Various Artists" ||
		rel.PrimaryType != "Album" || len(rel.SecondaryTypes) != 2 {
		t.Fatalf("Release = %+v", rel)
	}
	if len(rel.Media) != 1 || rel.Media[0].Position != 1 || len(rel.Media[0].Tracks) != 1 ||
		rel.Media[0].Tracks[0].Position != 7 || rel.Media[0].Tracks[0].Number != "7" {
		t.Fatalf("Media = %+v", rel.Media)
	}

	n, err := c.MediumCount(ctx, rel.ID)
	if err != nil || n != 2 {
		t.Fatalf("MediumCount = %d, %v", n, err)
	}

	// The cached response of MediumCount has no tracks, so Release must not
	// use it.
	if n, err = c.MediumCount(ctx, "b84ee12a-09ef-421b-82de-0441a926375b"); err != nil || n != 1 {
		t.Fatalf("MediumCount = %d, %v", n, err)
	}
	full, ok, err := c.Release(ctx, "b84ee12a-09ef-421b-82de-0441a926375b")
	if err != nil || !ok {
		t.Fatalf("Release = %v, %v", ok, err)
	}
	if full.Title != "The Dark Side of the Moon" || full.Artist != "Pink Floyd" || full.Date != "1973-03-24" || len(full.Media) != 1 {
		t.Fatalf("Release = %+v", full)
	}
	want := []Track{
		{Number: "A1", Position: 1, Title: "Speak to Me", RecordingID: "bef3fddb-5aca-49f5-b2fd-d56a23268d63", Length: 68346 * time.Millisecond},
		{Number: "A2", Position: 2, Title: "Breathe", RecordingID: "r2"},
	}
	if !slices.Equal(full.Media[0].Tracks, want) {
		t.Fatalf("Tracks = %+v, want %+v", full.Media[0].Tracks, want)
	}

	if _, ok, err := c.Recording(ctx, "nope"); err != nil || ok {
		t.Fatalf("Recording(unknown) = %v, %v", ok, err)
	}
	if _, ok, err := c.Release(ctx, "nope"); err != nil || ok {
		t.Fatalf("Release(unknown) = %v, %v", ok, err)
	}
}
