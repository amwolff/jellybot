package srrdb

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/amwolff/jellybot/internal/provider/providertest"
)

func fixtureServer(t *testing.T) string {
	t.Helper()
	search, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	details, err := os.ReadFile("testdata/details.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/archive-size:4690333463":
			_, _ = w.Write(search)
		case "/search/archive-size:1":
			_, _ = w.Write([]byte(`{"results":[],"resultsCount":0,"warnings":[],"query":["1"]}`))
		case "/details/Cube.1997.720p.BluRay.X264-AMIABLE":
			_, _ = w.Write(details)
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	return srv.URL
}

func TestSearchSize(t *testing.T) {
	c := New(providertest.Client(t))
	c.BaseURL = fixtureServer(t)

	got, total, err := c.SearchSize(context.Background(), 4690333463)
	if err != nil {
		t.Fatal(err)
	}
	want := []Release{{Name: "Cube.1997.720p.BluRay.X264-AMIABLE", IMDbID: "tt0123755"}}
	if !reflect.DeepEqual(got, want) || total != 1 {
		t.Fatalf("SearchSize = %+v, %d", got, total)
	}

	got, total, err = c.SearchSize(context.Background(), 1)
	if err != nil || len(got) != 0 || total != 0 {
		t.Fatalf("SearchSize(1) = %+v, %d, %v", got, total, err)
	}
}

func TestArchivedFiles(t *testing.T) {
	c := New(providertest.Client(t))
	c.BaseURL = fixtureServer(t)

	got, err := c.ArchivedFiles(context.Background(), "Cube.1997.720p.BluRay.X264-AMIABLE")
	if err != nil {
		t.Fatal(err)
	}
	want := []ArchivedFile{{Name: "the.cube.1997.720p.bluray.x264-amiable.mkv", Size: 4690333463, CRC: 0x3D7C3182}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ArchivedFiles = %+v", got)
	}

	got, err = c.ArchivedFiles(context.Background(), "Does.Not.Exist-NOPE")
	if err != nil || len(got) != 0 {
		t.Fatalf("ArchivedFiles(unknown) = %+v, %v", got, err)
	}
}
