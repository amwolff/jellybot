package media

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.senan.xyz/taglib"
)

func TestClassify(t *testing.T) {
	tests := map[string]Kind{
		"the other movie with Mace Windu.mkv": Video,
		"EP1.MP4":                             Video,
		"song.flac":                           Audio,
		"song.M4A":                            Audio,
		"movie.en.forced.srt":                 Sidecar,
		"track.lrc":                           Sidecar,
		"movie.nfo":                           Other,
		"cover.jpg":                           Other,
		"noext":                               Other,
	}
	for name, want := range tests {
		if got := Classify(name); got != want {
			t.Errorf("Classify(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSidecarSuffix(t *testing.T) {
	tests := []struct {
		main, name string
		suffix     string
		ok         bool
	}{
		{"movie.mkv", "movie.srt", ".srt", true},
		{"movie.mkv", "movie.en.forced.srt", ".en.forced.srt", true},
		{"Movie.mkv", "movie.EN.srt", ".EN.srt", true},
		{"01 Song.flac", "01 Song.lrc", ".lrc", true},
		{"movie.mkv", "movie2.srt", "", false},
		{"movie.mkv", "movie.nfo", "", false},
		{"movie.mkv", "movie.mkv", "", false},
		{"movie.mkv", "other.srt", "", false},
		{"movie.mkv", "movie", "", false},
	}
	for _, tt := range tests {
		suffix, ok := SidecarSuffix(tt.main, tt.name)
		if suffix != tt.suffix || ok != tt.ok {
			t.Errorf("SidecarSuffix(%q, %q) = %q, %v; want %q, %v", tt.main, tt.name, suffix, ok, tt.suffix, tt.ok)
		}
	}
}

func TestNumberOf(t *testing.T) {
	tests := []struct {
		in       string
		n, total int
	}{
		{"3", 3, 0},
		{"3/12", 3, 12},
		{" 2 / 2 ", 2, 2},
		{"", 0, 0},
		{"A1", 0, 0},
	}
	for _, tt := range tests {
		n, total := numberOf(tt.in)
		if n != tt.n || total != tt.total {
			t.Errorf("numberOf(%q) = %d, %d", tt.in, n, total)
		}
	}
}

// writeWAV writes one second of silent 8 kHz mono PCM.
func writeWAV(t *testing.T, path string) {
	t.Helper()
	const rate, samples = 8000, 8000
	const size = samples * 2
	data := make([]byte, size)
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, 36+size)
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, 1) // mono
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
}

func TestReadTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "banger lol.wav")
	writeWAV(t, path)
	if err := taglib.WriteTags(path, map[string][]string{
		taglib.Artist:             {"Daft Punk"},
		taglib.AlbumArtist:        {"Daft Punk"},
		taglib.Album:              {"Discovery"},
		taglib.Title:              {"One More Time"},
		taglib.TrackNumber:        {"1/14"},
		taglib.DiscNumber:         {"1/1"},
		taglib.Date:               {"2001-03-12"},
		taglib.MusicBrainzTrackID: {"05cf5b1f-6103-4410-922a-ef41bab995d2"},
	}, 0); err != nil {
		t.Fatalf("WriteTags: %v", err)
	}

	got, err := ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Tags{
		Artist: "Daft Punk", AlbumArtist: "Daft Punk", Album: "Discovery", Title: "One More Time",
		Track: 1, Disc: 1, DiscTotal: 1, Year: 2001, MBRecording: "05cf5b1f-6103-4410-922a-ef41bab995d2",
	}
	if got != want {
		t.Fatalf("ReadTags =\n%+v\nwant\n%+v", got, want)
	}
	if !got.Complete() {
		t.Fatal("Complete = false")
	}
}

func TestReadTagsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untagged.wav")
	writeWAV(t, path)
	got, err := ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Complete() {
		t.Fatalf("Complete = true for %+v", got)
	}
}

func TestFingerprint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fpcalc")
	script := "#!/bin/sh\n[ \"$1\" = -json ] || exit 2\necho '{\"duration\": 201.6, \"fingerprint\": \"AQAAfake\"}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}

	fp, d, err := Fingerprint(context.Background(), fake, filepath.Join(dir, "x.mp3"))
	if err != nil || fp != "AQAAfake" || d != 202 {
		t.Fatalf("Fingerprint = %q, %d, %v", fp, d, err)
	}

	failing := filepath.Join(dir, "fpcalc-fail")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho 'ERROR: bad file' >&2\nexit 1\n"), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	if _, _, err := Fingerprint(context.Background(), failing, "x"); err == nil {
		t.Fatal("expected an error")
	}
}
