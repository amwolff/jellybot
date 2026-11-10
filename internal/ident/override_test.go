package ident

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	mbRelease = "11111111-1111-4111-8111-111111111111"
	mbRec1    = "aaaaaaaa-0000-4000-8000-000000000001"
	mbRec2    = "aaaaaaaa-0000-4000-8000-000000000002"
	mbRec3    = "aaaaaaaa-0000-4000-8000-000000000003"
)

func TestParseOverride(t *testing.T) {
	valid := []struct {
		name string
		want override
	}{
		{"tmdb:603", override{provider: "tmdb", id: "603"}},
		{"TMDB:1396:S01E02", override{provider: "tmdb", id: "1396", hasEpisode: true, season: 1, episode: 2}},
		{" imdb:tt0133093 ", override{provider: "imdb", id: "tt0133093"}},
		{"imdb:tt0903747:s0e105", override{provider: "imdb", id: "tt0903747", hasEpisode: true, season: 0, episode: 105}},
		{"musicbrainz:" + mbRelease, override{provider: "musicbrainz", id: mbRelease}},
		{"MusicBrainz:" + strings.ToUpper(mbRec1), override{provider: "musicbrainz", id: mbRec1}},
	}
	for _, tt := range valid {
		if got, ok, err := parseOverride(tt.name); !ok || err != nil || got != tt.want {
			t.Errorf("parseOverride(%q) = %+v, %v, %v; want %+v", tt.name, got, ok, err, tt.want)
		}
	}

	for _, name := range []string{"", "The Matrix", "tmdb", "tmdb 603", "tvdb:5", "Mission: Impossible"} {
		if _, ok, _ := parseOverride(name); ok {
			t.Errorf("parseOverride(%q) took the name for an override", name)
		}
	}

	invalid := []string{
		"tmdb:", "tmdb:abc", "tmdb:0", "tmdb:603:e2", "tmdb:603:s01e02:x", "tmdb:12345678901",
		"imdb:133093", "imdb:tt", "musicbrainz:123", "musicbrainz:" + mbRec1 + ":s01e02",
	}
	for _, name := range invalid {
		if _, ok, err := parseOverride(name); !ok || err == nil {
			t.Errorf("parseOverride(%q) = %v, %v; want an invalid override", name, ok, err)
		}
	}
}

func TestVideoOverride(t *testing.T) {
	x, api := setup(t)
	dir := t.TempDir()
	api.routes["/tmdb/find/tt0133093"] = `{"movie_results":[{"id":603}],"tv_results":[],"tv_episode_results":[]}`
	api.routes["/tmdb/find/tt0959621"] = `{"movie_results":[],"tv_results":[],
		"tv_episode_results":[{"id":62085,"show_id":1396,"season_number":1,"episode_number":1}]}`

	movie := Identity{Kind: KindMovie, TMDBID: 603, Title: "The Matrix", Year: 1999}
	pilot := Identity{Kind: KindEpisode, TMDBID: 1396, Title: "Breaking Bad", Year: 2008, Season: 1, Episode: 1, EpisodeTitle: "Pilot"}
	tests := []struct {
		name string
		// A nil want means that the file must be unmatched.
		want *Identity
	}{
		{"tmdb:603.mkv", &movie},
		{"IMDb:tt0133093.mp4", &movie},
		{"tmdb:1396:s01e01.mkv", &pilot},
		{"imdb:tt0903747:S01E01.mkv", &pilot},
		{"imdb:tt0959621.mkv", &pilot},
		{"tmdb:999.mkv", nil},
		{"tmdb:999:s01e01.mkv", nil},
		{"tmdb:1396:s01e99.mkv", nil},
		{"imdb:tt0903747.mkv", nil},
		{"imdb:tt0133093:s01e01.mkv", nil},
		{"imdb:tt9999999.mkv", nil},
		{"tmdb:abc.mkv", nil},
		{"musicbrainz:" + mbRec1 + ".mkv", nil},
	}
	for i, tt := range tests {
		f := testFile(t, dir, tt.name, byte(i), 300_000)
		res, err := x.Identify(context.Background(), f, nil)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if tt.want == nil {
			if res.Identity != nil || res.Reason == "" {
				t.Errorf("%s: Identify = %+v, want a reason", tt.name, res)
			}
			continue
		}
		if res.Identity == nil {
			t.Errorf("%s: unmatched: %s", tt.name, res.Reason)
			continue
		}
		got := *res.Identity
		if got.Source != SourceName || !strings.HasPrefix(got.Detail, "file name ") {
			t.Errorf("%s: source %q, detail %q", tt.name, got.Source, got.Detail)
		}
		got.Source, got.Detail, got.IMDbID = "", "", ""
		if got != *tt.want {
			t.Errorf("%s: identity = %+v, want %+v", tt.name, got, *tt.want)
		}
	}

	if n := api.count("/os/") + api.count("/srr/"); n != 0 {
		t.Errorf("%d hash lookups for files with a name", n)
	}

	// A file without a hash needs only its name.
	res, err := x.Identify(context.Background(), testFile(t, t.TempDir(), "tmdb:603.mkv", 1, 1000), nil)
	if err != nil || res.Identity == nil || res.Identity.TMDBID != 603 {
		t.Fatalf("small file = %+v, %v", res, err)
	}
}

func TestVideoOverrideRecord(t *testing.T) {
	x, api := setup(t)
	dir := t.TempDir()
	plain := testFile(t, dir, "which one.mkv", 3, 300_000)
	api.routes["/os/subtitles?"+hashOf(t, plain)] = osBody(featureMatrix)

	res, err := x.Identify(context.Background(), plain, nil)
	if err != nil || res.Identity == nil || res.Identity.TMDBID != 603 {
		t.Fatalf("by hash = %+v, %v", res, err)
	}

	// The name goes before the recorded identity of the same contents.
	named := testFile(t, dir, "tmdb:604.mkv", 3, 300_000)
	res, err = x.Identify(context.Background(), named, nil)
	if err != nil || res.Identity == nil || res.Identity.TMDBID != 604 {
		t.Fatalf("by name = %+v, %v", res, err)
	}

	// The contents keep the identity under another name.
	res, err = x.Identify(context.Background(), plain, nil)
	if err != nil || res.Identity == nil || res.Identity.TMDBID != 604 || res.Identity.Source != SourceName {
		t.Fatalf("after the name = %+v, %v", res, err)
	}
}

type fakeTrack struct {
	recording string
	seconds   int
}

// releaseBody returns a MusicBrainz release with one medium for each element
// of discs. The title of a track is "Track" and its position in the release.
func releaseBody(id string, discs ...[]fakeTrack) string {
	var media []string
	n := 0
	for i, disc := range discs {
		var tracks []string
		for j, tr := range disc {
			n++
			length := "null"
			if tr.seconds > 0 {
				length = fmt.Sprint(tr.seconds * 1000)
			}
			tracks = append(tracks, fmt.Sprintf(`{"position":%d,"number":"%d","title":"Track %d","length":%s,"recording":{"id":%q}}`,
				j+1, j+1, n, length, tr.recording))
		}
		media = append(media, fmt.Sprintf(`{"position":%d,"track-count":%d,"tracks":[%s]}`, i+1, len(disc), strings.Join(tracks, ",")))
	}
	return fmt.Sprintf(`{"id":%q,"title":"Discovery","date":"2001-03-12","artist-credit":[{"name":"Daft Punk","joinphrase":""}],"media":[%s]}`,
		id, strings.Join(media, ","))
}

func TestAudioOverrideAlbum(t *testing.T) {
	type file struct {
		name    string
		seconds int
		// A zero track means that the file must be unmatched.
		disc, track int
		title       string
	}
	three := []fakeTrack{{mbRec1, 10}, {mbRec2, 20}, {mbRec3, 30}}
	tests := []struct {
		name   string
		folder string
		discs  [][]fakeTrack
		files  []file
	}{
		{
			name: "natural order", discs: [][]fakeTrack{three},
			files: []file{{"b10.wav", 30, 1, 3, "Track 3"}, {"B2.wav", 20, 1, 2, "Track 2"}, {"a.wav", 10, 1, 1, "Track 1"}},
		},
		{
			name: "a file is missing", discs: [][]fakeTrack{three},
			files: []file{{"1.wav", 10, 0, 0, ""}, {"2.wav", 20, 0, 0, ""}},
		},
		{
			name: "a file is not as long as its track", discs: [][]fakeTrack{three},
			files: []file{{"1.wav", 10, 1, 1, "Track 1"}, {"2.wav", 40, 0, 0, ""}, {"3.wav", 30, 1, 3, "Track 3"}},
		},
		{
			name: "a length that MusicBrainz does not know", discs: [][]fakeTrack{{{mbRec1, 10}, {mbRec2, 0}}},
			files: []file{{"1.wav", 10, 1, 1, "Track 1"}, {"2.wav", 40, 1, 2, "Track 2"}},
		},
		{
			name: "a file with its own recording", discs: [][]fakeTrack{three},
			files: []file{
				{"x.wav", 10, 1, 1, "Track 1"},
				{"musicbrainz:" + mbRec2 + ".wav", 40, 1, 2, "Track 2"},
				{"y.wav", 30, 1, 3, "Track 3"},
			},
		},
		{
			name: "a file with a recording of another release", discs: [][]fakeTrack{three},
			files: []file{{"musicbrainz:bbbbbbbb-0000-4000-8000-000000000001.wav", 10, 0, 0, ""}},
		},
		{
			name: "discs in directories", discs: [][]fakeTrack{{{mbRec1, 10}, {mbRec2, 20}}, {{mbRec3, 30}}},
			files: []file{
				{filepath.Join("CD2", "1.wav"), 30, 2, 1, "Track 3"},
				{filepath.Join("CD1", "2.wav"), 20, 1, 2, "Track 2"},
				{filepath.Join("CD1", "1.wav"), 10, 1, 1, "Track 1"},
			},
		},
		{
			name: "a folder name that is not valid", folder: "musicbrainz:discovery", discs: [][]fakeTrack{three},
			files: []file{{"1.wav", 10, 0, 0, ""}},
		},
		{
			name: "a release that MusicBrainz does not have", folder: "musicbrainz:22222222-2222-4222-8222-222222222222",
			files: []file{{"1.wav", 10, 0, 0, ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, api := setup(t)
			if tt.discs != nil {
				api.routes["/mb/release/"+mbRelease] = releaseBody(mbRelease, tt.discs...)
			}
			dir := filepath.Join(t.TempDir(), cmpOr(tt.folder, "MusicBrainz:"+mbRelease))

			var files []File
			for i, f := range tt.files {
				files = append(files, writeWAVOf(t, filepath.Join(dir, f.name), f.seconds, byte(i)))
			}
			// Jellyfin and other tools leave files that are not tracks.
			if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			writeWAVOf(t, filepath.Join(dir, ".hidden.wav"), 1, 0)

			for i, f := range tt.files {
				res, err := x.Identify(context.Background(), files[i], nil)
				if err != nil {
					t.Errorf("%s: %v", f.name, err)
					continue
				}
				if f.track == 0 {
					if res.Identity != nil || res.Reason == "" {
						t.Errorf("%s: Identify = %+v, want a reason", f.name, res)
					}
					continue
				}
				if res.Identity == nil {
					t.Errorf("%s: unmatched: %s", f.name, res.Reason)
					continue
				}
				id := *res.Identity
				if id.Disc != f.disc || id.Track != f.track || id.TrackTitle != f.title || id.DiscTotal != len(tt.discs) {
					t.Errorf("%s: disc %d of %d, track %d %q; want disc %d of %d, track %d %q",
						f.name, id.Disc, id.DiscTotal, id.Track, id.TrackTitle, f.disc, len(tt.discs), f.track, f.title)
				}
				if id.Kind != KindTrack || id.Artist != "Daft Punk" || id.Album != "Discovery" || id.Year != 2001 ||
					id.MBRelease != mbRelease || id.Source != SourceName {
					t.Errorf("%s: identity = %+v", f.name, id)
				}
			}
		})
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestAudioOverrideRecording(t *testing.T) {
	x, api := setup(t)
	dir := t.TempDir()
	api.routes["/mb/recording/"+mbRec2] = fmt.Sprintf(`{"id":%q,"title":"Aerodynamic","artist-credit":[{"name":"Daft Punk","joinphrase":""}],
		"releases":[{"id":%q,"title":"Discovery","status":"Official","date":"2001-03-12","artist-credit":[{"name":"Daft Punk","joinphrase":""}],
		 "release-group":{"primary-type":"Album","secondary-types":[]},
		 "media":[{"position":1,"track-count":3,"tracks":[{"number":"2","position":2,"title":"Aerodynamic"}]}]}]}`, mbRec2, mbRelease)
	api.routes["/mb/release/"+mbRelease] = releaseBody(mbRelease, []fakeTrack{{mbRec1, 10}, {mbRec2, 20}, {mbRec3, 30}})

	// The file has no tags, and jellybot has no fpcalc.
	named := writeWAVOf(t, filepath.Join(dir, "musicbrainz:"+mbRec2+".wav"), 10, 7)
	res, err := x.Identify(context.Background(), named, nil)
	if err != nil || res.Identity == nil {
		t.Fatalf("by name = %+v, %v", res, err)
	}
	id := *res.Identity
	if id.Artist != "Daft Punk" || id.Album != "Discovery" || id.TrackTitle != "Aerodynamic" || id.Track != 2 ||
		id.MBRecording != mbRec2 || id.MBRelease != mbRelease || id.Source != SourceName {
		t.Fatalf("identity = %+v", id)
	}

	// The contents keep the identity under another name.
	plain := writeWAVOf(t, filepath.Join(dir, "02 - Aerodynamic.wav"), 10, 7)
	res, err = x.Identify(context.Background(), plain, nil)
	if err != nil || res.Identity == nil || res.Identity.Track != 2 || res.Identity.Source != SourceName {
		t.Fatalf("after the name = %+v, %v", res, err)
	}

	for _, name := range []string{"tmdb:603.wav", "musicbrainz:nope.wav", "musicbrainz:bbbbbbbb-0000-4000-8000-000000000001.wav"} {
		res, err = x.Identify(context.Background(), writeWAVOf(t, filepath.Join(dir, name), 10, 8), nil)
		if err != nil || res.Identity != nil || res.Reason == "" {
			t.Errorf("%s: Identify = %+v, %v; want a reason", name, res, err)
		}
	}
}

func TestComparePaths(t *testing.T) {
	want := []string{
		"1.flac",
		"02.flac",
		"2.flac",
		"10.flac",
		filepath.Join("CD1", "2.flac"),
		filepath.Join("CD1", "10.flac"),
		filepath.Join("CD1 bonus", "1.flac"),
		filepath.Join("CD2", "1.flac"),
		filepath.Join("cd10", "1.flac"),
		"Intro.flac",
		"track 9.flac",
		"Track 10.flac",
	}
	got := slices.Clone(want)
	slices.Reverse(got)
	slices.SortFunc(got, comparePaths)
	if !slices.Equal(got, want) {
		t.Fatalf("order = %q, want %q", got, want)
	}
}
