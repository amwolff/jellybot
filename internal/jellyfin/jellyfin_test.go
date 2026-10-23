package jellyfin

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amwolff/jellybot/internal/ident"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"The Matrix", "The Matrix"},
		{"Mission: Impossible", "Mission - Impossible"},
		{"Star Wars:Episode IV", "Star Wars-Episode IV"},
		{"AC/DC", "AC+DC"},
		{`back\slash`, "back+slash"},
		{"What If...?", "What If...!"},
		{"M*A*S*H", "M-A-S-H"},
		{`<"Quoted"> | piped`, "Quoted piped"},
		{"tab\there\nnewline", "tab here newline"},
		{"  lots   of   space  ", "lots of space"},
		{"Trailing dots...", "Trailing dots"},
		{".hidden", "hidden"},
		{"Amélie", "Amélie"},
		{"", ""},
		{"???", "!!!"},
		{"...", ""},
	}
	for _, tt := range tests {
		if got := Sanitize(tt.in); got != tt.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPath(t *testing.T) {
	l := DefaultLayout
	tests := []struct {
		name string
		id   ident.Identity
		ext  string
		want string
	}{
		{
			name: "movie",
			id:   ident.Identity{Kind: ident.KindMovie, Title: "The Matrix", Year: 1999, TMDBID: 603},
			ext:  ".mkv",
			want: "Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv",
		},
		{
			name: "movie, upper-case extension, colon",
			id:   ident.Identity{Kind: ident.KindMovie, Title: "Mission: Impossible", Year: 1996, TMDBID: 954},
			ext:  ".MP4",
			want: "Movies/Mission - Impossible (1996) [tmdbid-954]/Mission - Impossible (1996) [tmdbid-954].mp4",
		},
		{
			name: "movie without year, IMDb only",
			id:   ident.Identity{Kind: ident.KindMovie, Title: "Cube", IMDbID: "tt0123755"},
			ext:  ".mkv",
			want: "Movies/Cube [imdbid-tt0123755]/Cube [imdbid-tt0123755].mkv",
		},
		{
			name: "episode",
			id: ident.Identity{
				Kind: ident.KindEpisode, Title: "Breaking Bad", Year: 2008, TMDBID: 1396,
				Season: 1, Episode: 1, EpisodeTitle: "Pilot",
			},
			ext:  ".mkv",
			want: "Shows/Breaking Bad (2008) [tmdbid-1396]/Season 01/Breaking Bad (2008) - S01E01 - Pilot.mkv",
		},
		{
			name: "special without title",
			id: ident.Identity{
				Kind: ident.KindEpisode, Title: "Doctor Who", Year: 2005, TMDBID: 57243,
				Season: 0, Episode: 3,
			},
			ext:  ".mkv",
			want: "Shows/Doctor Who (2005) [tmdbid-57243]/Season 00/Doctor Who (2005) - S00E03.mkv",
		},
		{
			name: "episode title with slash",
			id: ident.Identity{
				Kind: ident.KindEpisode, Title: "Show", Year: 2020, TMDBID: 1,
				Season: 2, Episode: 105, EpisodeTitle: "Part 1/2",
			},
			ext:  ".mkv",
			want: "Shows/Show (2020) [tmdbid-1]/Season 02/Show (2020) - S02E105 - Part 1+2.mkv",
		},
		{
			name: "track",
			id: ident.Identity{
				Kind: ident.KindTrack, Artist: "Daft Punk", Album: "Discovery", Year: 2001,
				TrackTitle: "One More Time", Track: 1, Disc: 1, DiscTotal: 1,
			},
			ext:  ".flac",
			want: "Music/Daft Punk/Discovery (2001)/01 - One More Time.flac",
		},
		{
			name: "track on second disc",
			id: ident.Identity{
				Kind: ident.KindTrack, Artist: "Pink Floyd", Album: "The Wall",
				TrackTitle: "Hey You", Track: 1, Disc: 2, DiscTotal: 2,
			},
			ext:  ".mp3",
			want: "Music/Pink Floyd/The Wall/2-01 - Hey You.mp3",
		},
		{
			name: "track on first disc of two",
			id: ident.Identity{
				Kind: ident.KindTrack, Artist: "Pink Floyd", Album: "The Wall",
				TrackTitle: "In the Flesh?", Track: 1, Disc: 1, DiscTotal: 2,
			},
			ext:  ".mp3",
			want: "Music/Pink Floyd/The Wall/1-01 - In the Flesh!.mp3",
		},
		{
			name: "track without number",
			id:   ident.Identity{Kind: ident.KindTrack, Artist: "A", Album: "B", TrackTitle: "C"},
			ext:  ".ogg",
			want: "Music/A/B/C.ogg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := l.Path(tt.id, tt.ext)
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if want := filepath.FromSlash(tt.want); got != want {
				t.Fatalf("Path =\n  %s\nwant\n  %s", got, want)
			}
		})
	}
}

func TestPathErrors(t *testing.T) {
	tests := []ident.Identity{
		{Kind: ident.KindMovie, Title: ""},
		{Kind: ident.KindMovie, Title: "..."},
		{Kind: ident.KindEpisode, Title: "Show", Season: 1, Episode: 0},
		{Kind: ident.KindTrack, Artist: "A", TrackTitle: "C"},
		{Kind: "nope", Title: "x"},
	}
	for _, id := range tests {
		if _, err := DefaultLayout.Path(id, ".mkv"); err == nil {
			t.Errorf("Path(%+v) succeeded, want an error", id)
		}
	}
}

func TestPathLongTitle(t *testing.T) {
	title := strings.Repeat("é", 300) // 600 bytes
	got, err := DefaultLayout.Path(ident.Identity{Kind: ident.KindMovie, Title: title, Year: 2000, TMDBID: 123456}, ".mkv")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range strings.Split(got, string(filepath.Separator)) {
		if len(part) > 255 {
			t.Fatalf("component has %d bytes", len(part))
		}
		if !utf8.ValidString(part) {
			t.Fatalf("component %q is not valid UTF-8", part)
		}
	}
	dir, file := filepath.Split(got)
	folder := filepath.Base(dir)
	if !strings.HasSuffix(folder, "[tmdbid-123456]") {
		t.Fatalf("folder %q lost its ID", folder)
	}
	if file != folder+".mkv" {
		t.Fatalf("file %q does not start with the folder name %q", file, folder)
	}
}

func TestUnidentifiedAndSidecar(t *testing.T) {
	rel := filepath.Join("sub", "the other movie with Mace Windu.mkv")
	got := DefaultLayout.UnidentifiedPath(rel)
	if want := filepath.Join("Unidentified", "sub", "the other movie with Mace Windu.mkv"); got != want {
		t.Fatalf("UnidentifiedPath = %q, want %q", got, want)
	}

	main := filepath.FromSlash("Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv")
	if got, want := SidecarPath(main, ".en.forced.srt"), filepath.FromSlash("Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].en.forced.srt"); got != want {
		t.Fatalf("SidecarPath = %q, want %q", got, want)
	}
}

func TestLayoutValidate(t *testing.T) {
	if err := DefaultLayout.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "/abs", "../up", "a/../../b"} {
		l := DefaultLayout
		l.Music = bad
		if err := l.Validate(); err == nil {
			t.Errorf("Validate accepted %q", bad)
		}
	}
	l := DefaultLayout
	l.Movies = "Media/Films"
	if err := l.Validate(); err != nil {
		t.Errorf("Validate rejected a nested directory: %v", err)
	}
}
