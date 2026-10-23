// Package media classifies files and reads what the files say about
// themselves: embedded tags and audio fingerprints.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"go.senan.xyz/taglib"
)

// Kind is the kind of a file.
type Kind int

// Kinds of files.
const (
	Other Kind = iota
	Video
	Audio
	Sidecar
)

func set(exts ...string) map[string]bool {
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		m[e] = true
	}
	return m
}

var (
	videoExts   = set(".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".ts", ".m2ts", ".webm", ".mpg", ".mpeg", ".ogm")
	audioExts   = set(".mp3", ".flac", ".m4a", ".aac", ".ogg", ".oga", ".opus", ".wav", ".wma", ".ape", ".wv", ".aiff", ".aif", ".dsf", ".mka")
	sidecarExts = set(".srt", ".ass", ".ssa", ".vtt", ".sub", ".idx", ".sup", ".lrc", ".elrc")
)

// Classify returns the kind of the file with name.
func Classify(name string) Kind {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case videoExts[ext]:
		return Video
	case audioExts[ext]:
		return Audio
	case sidecarExts[ext]:
		return Sidecar
	}
	return Other
}

// SidecarSuffix reports whether name is a sidecar of the main file
// mainName in the same directory, and returns its suffix: the part after
// the base name of the main file, for example ".en.forced.srt". The match
// of the base name ignores case.
func SidecarSuffix(mainName, name string) (string, bool) {
	if Classify(name) != Sidecar {
		return "", false
	}
	base := strings.TrimSuffix(mainName, filepath.Ext(mainName))
	if len(name) <= len(base) || !strings.EqualFold(name[:len(base)], base) || name[len(base)] != '.' {
		return "", false
	}
	return name[len(base):], true
}

// Tags are the embedded tags of an audio file that jellybot uses.
type Tags struct {
	Artist      string
	AlbumArtist string
	Album       string
	Title       string
	Track       int
	Disc        int
	DiscTotal   int
	Year        int
	MBRecording string
	MBRelease   string
}

// Complete reports whether the tags name an artist, an album, and a title.
func (t Tags) Complete() bool {
	return (t.AlbumArtist != "" || t.Artist != "") && t.Album != "" && t.Title != ""
}

// ReadTags reads the embedded tags of the audio file at path.
func ReadTags(path string) (Tags, error) {
	raw, err := taglib.ReadTags(path)
	if err != nil {
		return Tags{}, fmt.Errorf("media: read tags: %w", err)
	}

	first := func(keys ...string) string {
		for _, k := range keys {
			for _, v := range raw[k] {
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			}
		}
		return ""
	}

	t := Tags{
		Artist:      first(taglib.Artist),
		AlbumArtist: first(taglib.AlbumArtist),
		Album:       first(taglib.Album),
		Title:       first(taglib.Title),
		Year:        year(first(taglib.Date, taglib.OriginalDate, taglib.ReleaseDate)),
		MBRecording: first(taglib.MusicBrainzTrackID),
		MBRelease:   first(taglib.MusicBrainzAlbumID),
	}
	t.Track, _ = numberOf(first(taglib.TrackNumber))
	var total int
	t.Disc, total = numberOf(first(taglib.DiscNumber))
	if n, _ := numberOf(first("DISCTOTAL", "TOTALDISCS")); n > 0 {
		total = n
	}
	t.DiscTotal = total
	return t, nil
}

// numberOf parses "3" or "3/12".
func numberOf(s string) (n, total int) {
	a, b, _ := strings.Cut(s, "/")
	n, _ = strconv.Atoi(strings.TrimSpace(a))
	total, _ = strconv.Atoi(strings.TrimSpace(b))
	return max(n, 0), max(total, 0)
}

func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}

// ErrNoFingerprint is returned when fpcalc does not give a fingerprint.
var ErrNoFingerprint = errors.New("media: fpcalc gave no fingerprint")

// Fingerprint runs fpcalc (the Chromaprint tool) on the audio file at path.
// It returns the fingerprint and the duration in whole seconds.
func Fingerprint(ctx context.Context, fpcalc, path string) (string, int, error) {
	var stdout, stderr bytes.Buffer
	// fpcalc comes from exec.LookPath or from the tests.
	cmd := exec.CommandContext(ctx, fpcalc, "-json", path) //nolint:gosec // see above
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("media: fpcalc: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var out struct {
		Duration    float64 `json:"duration"`
		Fingerprint string  `json:"fingerprint"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return "", 0, fmt.Errorf("media: fpcalc: %w", err)
	}
	if out.Fingerprint == "" {
		return "", 0, ErrNoFingerprint
	}
	return out.Fingerprint, int(math.Round(out.Duration)), nil
}
