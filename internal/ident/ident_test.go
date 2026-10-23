package ident

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/amwolff/jellybot/internal/media"
	"github.com/amwolff/jellybot/internal/oshash"
	"github.com/amwolff/jellybot/internal/provider/acoustid"
	"github.com/amwolff/jellybot/internal/provider/musicbrainz"
	"github.com/amwolff/jellybot/internal/provider/opensubtitles"
	"github.com/amwolff/jellybot/internal/provider/providertest"
	"github.com/amwolff/jellybot/internal/provider/srrdb"
	"github.com/amwolff/jellybot/internal/provider/tmdb"
	"github.com/amwolff/jellybot/internal/store"
)

// testFile writes a file of size bytes whose contents depend on seed.
func testFile(t *testing.T, dir, name string, seed byte, size int) File {
	t.Helper()
	data := bytes.Repeat([]byte{seed, seed + 1, seed + 2, 7}, size/4+1)[:size]
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return File{Path: path, Size: fi.Size(), ModTime: fi.ModTime(), Kind: media.Classify(name)}
}

func hashOf(t *testing.T, f File) string {
	t.Helper()
	h, _, err := oshash.File(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

func crcOf(t *testing.T, f File) string {
	t.Helper()
	data, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%08X", crc32.ChecksumIEEE(data))
}

type fakeAPI struct {
	mu     sync.Mutex
	calls  map[string]int
	routes map[string]string // path (with query for OpenSubtitles) → body
}

func (f *fakeAPI) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, v := range f.calls {
		if strings.HasPrefix(k, prefix) {
			n += v
		}
	}
	return n
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path
	if strings.HasPrefix(key, "/os/") {
		key += "?" + r.URL.Query().Get("moviehash")
	}
	if strings.HasPrefix(key, "/acoustid/") {
		_ = r.ParseForm()
		key += "?" + r.PostForm.Get("fingerprint")
	}
	f.mu.Lock()
	f.calls[key]++
	body, ok := f.routes[key]
	f.mu.Unlock()

	if ok {
		_, _ = w.Write([]byte(body))
		return
	}
	switch {
	case strings.HasPrefix(key, "/os/"):
		_, _ = w.Write([]byte(`{"data":[]}`))
	case strings.HasPrefix(key, "/srr/search/"):
		_, _ = w.Write([]byte(`{"results":[],"resultsCount":0}`))
	case strings.HasPrefix(key, "/acoustid/"):
		_, _ = w.Write([]byte(`{"status":"ok","results":[]}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*Identifier, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{calls: map[string]int{}, routes: map[string]string{}}
	srv := providertest.Server(t, api.ServeHTTP)
	p := providertest.Client(t)

	osc := opensubtitles.New(p, "k")
	osc.BaseURL = srv.URL + "/os"
	srr := srrdb.New(p)
	srr.BaseURL = srv.URL + "/srr"
	tm := tmdb.New(p, "k")
	tm.BaseURL = srv.URL + "/tmdb"
	ac := acoustid.New(p, "k")
	ac.BaseURL = srv.URL + "/acoustid"
	mb := musicbrainz.New(p)
	mb.BaseURL = srv.URL + "/mb"

	api.routes["/tmdb/movie/603"] = `{"id":603,"title":"The Matrix","release_date":"1999-03-30","imdb_id":"tt0133093"}`
	api.routes["/tmdb/movie/604"] = `{"id":604,"title":"The Matrix Reloaded","release_date":"2003-05-15","imdb_id":"tt0234215"}`
	api.routes["/tmdb/movie/431"] = `{"id":431,"title":"Cube","release_date":"1997-09-09","imdb_id":"tt0123755"}`
	api.routes["/tmdb/tv/1396"] = `{"id":1396,"name":"Breaking Bad","first_air_date":"2008-01-20"}`
	api.routes["/tmdb/tv/1396/season/1/episode/1"] = `{"id":62085,"name":"Pilot","season_number":1,"episode_number":1}`
	api.routes["/tmdb/find/tt0123755"] = `{"movie_results":[{"id":431}],"tv_results":[],"tv_episode_results":[]}`
	api.routes["/tmdb/find/tt0903747"] = `{"movie_results":[],"tv_results":[{"id":1396}],"tv_episode_results":[]}`

	return &Identifier{
		Store:         p.Store,
		OpenSubtitles: osc,
		SRRDB:         srr,
		TMDB:          tm,
		AcoustID:      ac,
		MusicBrainz:   mb,
	}, api
}

func osBody(features ...string) string {
	var data []string
	for _, f := range features {
		data = append(data, `{"attributes":{"moviehash_match":true,"feature_details":`+f+`}}`)
	}
	return `{"data":[` + strings.Join(data, ",") + `]}`
}

const (
	featureMatrix   = `{"feature_id":1,"feature_type":"Movie","title":"The Matrix","year":1999,"imdb_id":133093,"tmdb_id":603}`
	featureReloaded = `{"feature_id":2,"feature_type":"Movie","title":"The Matrix Reloaded","year":2003,"imdb_id":234215,"tmdb_id":604}`
	featurePilot    = `{"feature_id":3,"feature_type":"Episode","title":"Pilot","year":2008,"imdb_id":959621,"tmdb_id":62085,
		"parent_title":"Breaking Bad","parent_imdb_id":903747,"parent_tmdb_id":1396,"season_number":1,"episode_number":1}`
)

func TestVideoOpenSubtitlesMovie(t *testing.T) {
	x, api := setup(t)
	f := testFile(t, t.TempDir(), "the other movie with Mace Windu.mkv", 1, 300_000)
	api.routes["/os/subtitles?"+hashOf(t, f)] = osBody(featureMatrix, featureMatrix)

	var stages []string
	res, err := x.Identify(context.Background(), f, func(s string) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Identity == nil {
		t.Fatalf("no identity: %+v", res)
	}
	id := *res.Identity
	if id.Kind != KindMovie || id.TMDBID != 603 || id.Title != "The Matrix" || id.Year != 1999 || id.Source != SourceOpenSubtitles {
		t.Fatalf("identity = %+v", id)
	}
	if !strings.Contains(id.Detail, "OpenSubtitles hash "+hashOf(t, f)) || !strings.Contains(id.Detail, "tmdb 603") {
		t.Fatalf("detail = %q", id.Detail)
	}
	if stages[0] != StageHashing || stages[1] != StageOpenSubtitles {
		t.Fatalf("stages = %v", stages)
	}

	// The second identification uses the recorded identity, not
	// OpenSubtitles.
	before := api.count("/os/")
	res, err = x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil || res.Identity.TMDBID != 603 {
		t.Fatalf("second Identify = %+v, %v", res, err)
	}
	if api.count("/os/") != before {
		t.Fatal("OpenSubtitles was called again")
	}

	// A refresh ignores the recorded identity and the cache.
	if _, err := x.Identify(store.WithRefresh(context.Background()), f, nil); err != nil {
		t.Fatal(err)
	}
	if api.count("/os/") != before+1 {
		t.Fatal("refresh did not call OpenSubtitles")
	}
}

func TestVideoOpenSubtitlesEpisode(t *testing.T) {
	x, api := setup(t)
	f := testFile(t, t.TempDir(), "ep1 lol.mkv", 2, 300_000)
	api.routes["/os/subtitles?"+hashOf(t, f)] = osBody(featurePilot)

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil {
		t.Fatalf("Identify = %+v, %v", res, err)
	}
	id := *res.Identity
	if id.Kind != KindEpisode || id.TMDBID != 1396 || id.Title != "Breaking Bad" || id.Year != 2008 ||
		id.Season != 1 || id.Episode != 1 || id.EpisodeTitle != "Pilot" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestVideoAmbiguous(t *testing.T) {
	x, api := setup(t)
	f := testFile(t, t.TempDir(), "which one.mkv", 3, 300_000)
	api.routes["/os/subtitles?"+hashOf(t, f)] = osBody(featureMatrix, featureReloaded)

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Identity != nil || len(res.Candidates) != 2 || !res.HasKey {
		t.Fatalf("result = %+v", res)
	}
	if res.Candidates[0].Title != "The Matrix" || res.Candidates[1].Title != "The Matrix Reloaded" {
		t.Fatalf("candidates = %+v", res.Candidates)
	}

	// The choice of the user becomes the identity.
	if err = x.Choose(res.Key, res.Candidates[1]); err != nil {
		t.Fatal(err)
	}
	res, err = x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil || res.Identity.TMDBID != 604 || res.Identity.Source != SourceUser {
		t.Fatalf("after Choose = %+v, %v", res, err)
	}
}

func TestVideoSRRDBMovie(t *testing.T) {
	x, api := setup(t)
	f := testFile(t, t.TempDir(), "that cube thing.mkv", 4, 400_001)
	size := fmt.Sprint(f.Size)
	api.routes["/srr/search/archive-size:"+size] = `{"results":[
		{"release":"Other.Release-GRP","imdbId":"0000001"},
		{"release":"Cube.1997.720p.BluRay.X264-AMIABLE","imdbId":"0123755"}],"resultsCount":2}`
	api.routes["/srr/details/Other.Release-GRP"] = `{"archived-files":[{"name":"x.mkv","size":` + size + `,"crc":"00000000"}]}`
	api.routes["/srr/details/Cube.1997.720p.BluRay.X264-AMIABLE"] = `{"archived-files":[{"name":"cube.mkv","size":` + size + `,"crc":"` + crcOf(t, f) + `"}]}`

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil {
		t.Fatalf("Identify = %+v, %v", res, err)
	}
	id := *res.Identity
	if id.Kind != KindMovie || id.TMDBID != 431 || id.Title != "Cube" || id.Year != 1997 || id.Source != SourceSRRDB {
		t.Fatalf("identity = %+v", id)
	}
	if !strings.Contains(id.Detail, "Cube.1997.720p.BluRay.X264-AMIABLE") {
		t.Fatalf("detail = %q", id.Detail)
	}

	// The CRC32 is in the memo now.
	st, ok, err := x.Store.Stat(f.Path)
	if err != nil || !ok || st.CRC32 == nil {
		t.Fatalf("stat memo = %+v, %v, %v", st, ok, err)
	}
}

func TestVideoSRRDBEpisode(t *testing.T) {
	x, api := setup(t)
	f := testFile(t, t.TempDir(), "bb.mkv", 5, 400_003)
	size := fmt.Sprint(f.Size)
	api.routes["/srr/search/archive-size:"+size] = `{"results":[{"release":"Breaking.Bad.S01E01.720p.BluRay.x264-GRP","imdbId":"0903747"}],"resultsCount":1}`
	api.routes["/srr/details/Breaking.Bad.S01E01.720p.BluRay.x264-GRP"] = `{"archived-files":[{"name":"bb.mkv","size":` + size + `,"crc":"` + crcOf(t, f) + `"}]}`

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil {
		t.Fatalf("Identify = %+v, %v", res, err)
	}
	id := *res.Identity
	if id.Kind != KindEpisode || id.TMDBID != 1396 || id.Season != 1 || id.Episode != 1 || id.EpisodeTitle != "Pilot" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestVideoSRRDBTooManyResults(t *testing.T) {
	x, api := setup(t)
	f := testFile(t, t.TempDir(), "common.mkv", 6, 200_000)
	api.routes["/srr/search/archive-size:200000"] = `{"results":[{"release":"A-B"}],"resultsCount":3380}`

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity != nil {
		t.Fatalf("Identify = %+v, %v", res, err)
	}
	if !strings.Contains(res.Reason, "3380 releases") {
		t.Fatalf("reason = %q", res.Reason)
	}
	if api.count("/srr/details/") != 0 {
		t.Fatal("details were fetched")
	}
}

func TestVideoUnmatched(t *testing.T) {
	x, _ := setup(t)
	dir := t.TempDir()

	res, err := x.Identify(context.Background(), testFile(t, dir, "home video 2014.mp4", 7, 300_000), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Identity != nil || res.Reason != "OpenSubtitles: no match; srrDB: no match" {
		t.Fatalf("result = %+v", res)
	}

	res, err = x.Identify(context.Background(), testFile(t, dir, "tiny.mkv", 8, 1000), nil)
	if err != nil || res.Reason != "file is smaller than 128 KiB" {
		t.Fatalf("tiny = %+v, %v", res, err)
	}

	x.TMDB = nil
	res, err = x.Identify(context.Background(), testFile(t, dir, "x.mkv", 9, 300_000), nil)
	if err != nil || !strings.HasPrefix(res.Reason, "no TMDB key") {
		t.Fatalf("no TMDB = %+v, %v", res, err)
	}
}

func TestVideoNoOpenSubtitlesKey(t *testing.T) {
	x, _ := setup(t)
	x.OpenSubtitles = nil
	res, err := x.Identify(context.Background(), testFile(t, t.TempDir(), "x.mkv", 10, 300_000), nil)
	if err != nil || res.Reason != "no OpenSubtitles key; srrDB: no match" {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

// writeWAV writes ten seconds of silent 8 kHz mono PCM.
func writeWAV(t *testing.T, path string) File {
	t.Helper()
	const rate = 8000
	const size = 10 * rate * 2
	data := make([]byte, size)
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, 36+size)
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, rate)
	b = binary.LittleEndian.AppendUint32(b, rate*2)
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, size)
	b = append(b, data...)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return File{Path: path, Size: fi.Size(), ModTime: fi.ModTime(), Kind: media.Audio}
}

func TestAudioTags(t *testing.T) {
	x, _ := setup(t)
	f := writeWAV(t, filepath.Join(t.TempDir(), "banger lol.wav"))
	if err := taglib.WriteTags(f.Path, map[string][]string{
		taglib.Artist: {"Daft Punk"}, taglib.Album: {"Discovery"}, taglib.Title: {"One More Time"},
		taglib.TrackNumber: {"1"}, taglib.Date: {"2001"},
	}, 0); err != nil {
		t.Fatal(err)
	}

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil {
		t.Fatalf("Identify = %+v, %v", res, err)
	}
	id := *res.Identity
	if id.Kind != KindTrack || id.Artist != "Daft Punk" || id.Album != "Discovery" || id.TrackTitle != "One More Time" ||
		id.Track != 1 || id.Year != 2001 || id.Source != SourceTags {
		t.Fatalf("identity = %+v", id)
	}
}

func TestAudioUntagged(t *testing.T) {
	x, _ := setup(t)
	f := writeWAV(t, filepath.Join(t.TempDir(), "mystery.wav"))

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Reason != "incomplete tags; install fpcalc for fingerprints" {
		t.Fatalf("no fpcalc = %+v, %v", res, err)
	}

	x.FPCalc = "/bin/true"
	x.AcoustID = nil
	res, err = x.Identify(context.Background(), f, nil)
	if err != nil || res.Reason != "incomplete tags; no AcoustID key" {
		t.Fatalf("no AcoustID = %+v, %v", res, err)
	}
}

func TestAudioAcoustID(t *testing.T) {
	x, api := setup(t)
	dir := t.TempDir()
	f := writeWAV(t, filepath.Join(dir, "mystery.wav"))

	fake := filepath.Join(dir, "fpcalc")
	script := "#!/bin/sh\necho '{\"duration\": 10.0, \"fingerprint\": \"AQAAmystery\"}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	x.FPCalc = fake

	api.routes["/acoustid/lookup?AQAAmystery"] = `{"status":"ok","results":[{"id":"a","score":0.95,"recordings":[{"id":"rec-1"}]}]}`
	api.routes["/mb/recording/rec-1"] = `{"id":"rec-1","title":"One More Time","artist-credit":[{"name":"Daft Punk","joinphrase":""}],
		"releases":[
		 {"id":"rel-comp","title":"Hits","status":"Official","date":"1999","artist-credit":[{"name":"Various Artists","joinphrase":""}],
		  "release-group":{"primary-type":"Album","secondary-types":["Compilation"]},
		  "media":[{"position":1,"track-count":20,"tracks":[{"number":"5","position":5,"title":"One More Time"}]}]},
		 {"id":"rel-album","title":"Discovery","status":"Official","date":"2001-03-12","artist-credit":[{"name":"Daft Punk","joinphrase":""}],
		  "release-group":{"primary-type":"Album","secondary-types":[]},
		  "media":[{"position":1,"track-count":14,"tracks":[{"number":"1","position":1,"title":"One More Time"}]}]}]}`
	api.routes["/mb/release/rel-album"] = `{"media":[{"position":1}]}`

	res, err := x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil {
		t.Fatalf("Identify = %+v, %v", res, err)
	}
	id := *res.Identity
	if id.Kind != KindTrack || id.Artist != "Daft Punk" || id.Album != "Discovery" || id.Year != 2001 ||
		id.Track != 1 || id.Disc != 1 || id.DiscTotal != 1 || id.MBRelease != "rel-album" || id.Source != SourceAcoustID {
		t.Fatalf("identity = %+v", id)
	}

	// The second identification uses the recorded identity and does not
	// call AcoustID.
	if err = os.Remove(fake); err != nil {
		t.Fatal(err)
	}
	x.FPCalc = "/nonexistent/fpcalc"
	before := api.count("/acoustid/")
	res, err = x.Identify(context.Background(), f, nil)
	if err != nil || res.Identity == nil || res.Identity.MBRelease != "rel-album" {
		t.Fatalf("second Identify = %+v, %v", res, err)
	}
	if api.count("/acoustid/") != before {
		t.Fatal("AcoustID was called again")
	}
}

func TestChooseRelease(t *testing.T) {
	track := []musicbrainz.Medium{{Position: 1, Tracks: []musicbrainz.Track{{Position: 1}}}}
	rel := func(id, title, status, date, primary string, secondary ...string) musicbrainz.Release {
		return musicbrainz.Release{ID: id, Title: title, Status: status, Date: date, PrimaryType: primary,
			SecondaryTypes: secondary, Media: track}
	}
	releases := []musicbrainz.Release{
		rel("e", "Bootleg", "Bootleg", "1990", "Album"),
		rel("d", "Single", "Official", "2000", "Single"),
		rel("c", "Hits", "Official", "1995", "Album", "Compilation"),
		rel("b", "Discovery", "Official", "", "Album"),
		rel("a", "Discovery", "Official", "2001-03-12", "Album"),
		rel("z", "Discovery (Remaster)", "Official", "2001-03-12", "Album"),
		{ID: "0", Title: "No track", Status: "Official", Date: "1980", PrimaryType: "Album"},
	}

	tests := []struct {
		hint string
		want string
	}{
		{"", "a"},
		{"hits", "c"},
		{"Bootleg", "e"},
		{"Unknown Album", "a"},
	}
	for _, tt := range tests {
		got, ok := ChooseRelease(releases, tt.hint)
		if !ok || got.ID != tt.want {
			t.Errorf("ChooseRelease(hint %q) = %q, want %q", tt.hint, got.ID, tt.want)
		}
	}

	if _, ok := ChooseRelease(releases[6:], ""); ok {
		t.Error("ChooseRelease accepted a release without the track")
	}
}

func TestEpisodeTag(t *testing.T) {
	tests := map[string][]string{
		"Breaking.Bad.S01E01.720p.BluRay.x264-GRP": {"01", "01"},
		"show.s10e105.hdtv-x":                      {"10", "105"},
		"Show.S02E03E04.720p-GRP":                  {"02", "03"},
		"Movie.2001.720p-GRP":                      nil,
	}
	for in, want := range tests {
		m := episodeTag.FindStringSubmatch(in)
		if want == nil {
			if m != nil {
				t.Errorf("%q matched %v", in, m)
			}
			continue
		}
		if m == nil || m[1] != want[0] || m[2] != want[1] {
			t.Errorf("%q = %v, want %v", in, m, want)
		}
	}
}
