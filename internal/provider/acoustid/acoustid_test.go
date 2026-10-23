package acoustid

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/amwolff/jellybot/internal/provider/providertest"
)

func TestLookup(t *testing.T) {
	srv := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/lookup" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("client") != "key" || r.PostForm.Get("meta") != "recordingids" {
			t.Errorf("form = %v", r.PostForm)
		}
		switch r.PostForm.Get("fingerprint") {
		case "AQAAgood":
			if r.PostForm.Get("duration") != "201" {
				t.Errorf("duration = %q", r.PostForm.Get("duration"))
			}
			_, _ = w.Write([]byte(`{"status":"ok","results":[
				{"id":"low","score":0.4,"recordings":[{"id":"r-low"}]},
				{"id":"high","score":0.97,"recordings":[{"id":"r1"},{"id":"r2"}]}]}`))
		case "AQAAnone":
			_, _ = w.Write([]byte(`{"status":"ok","results":[]}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","error":{"code":3,"message":"invalid fingerprint"}}`))
		}
	})
	c := New(providertest.Client(t), "key")
	c.BaseURL = srv.URL
	ctx := context.Background()

	got, err := c.Lookup(ctx, "AQAAgood", 201)
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{
		{ID: "high", Score: 0.97, Recordings: []string{"r1", "r2"}},
		{ID: "low", Score: 0.4, Recordings: []string{"r-low"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup = %+v", got)
	}

	got, err = c.Lookup(ctx, "AQAAnone", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("Lookup(none) = %+v, %v", got, err)
	}

	if _, err := c.Lookup(ctx, "bad", 10); err == nil {
		t.Fatal("expected an error")
	}
}
