package musicbrainz

import (
	"context"
	"net/http"
	"os"
	"testing"

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

	if _, ok, err := c.Recording(ctx, "nope"); err != nil || ok {
		t.Fatalf("Recording(unknown) = %v, %v", ok, err)
	}
}
