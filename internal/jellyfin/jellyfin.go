// Package jellyfin builds destination paths that follow the Jellyfin naming
// conventions.
package jellyfin

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/amwolff/jellybot/internal/ident"
)

// MaxName is the byte limit of one path component on common filesystems
// (ext4, XFS, Btrfs, and ZFS without the longname property).
const MaxName = 255

// Layout holds the library subdirectories under the destination root.
type Layout struct {
	Movies       string
	Shows        string
	Music        string
	Unidentified string
}

// DefaultLayout is the layout without flag overrides.
var DefaultLayout = Layout{
	Movies:       "Movies",
	Shows:        "Shows",
	Music:        "Music",
	Unidentified: "Unidentified",
}

// Validate makes sure that each subdirectory is a relative path inside the
// destination root.
func (l Layout) Validate() error {
	for _, dir := range []string{l.Movies, l.Shows, l.Music, l.Unidentified} {
		if dir == "" || filepath.IsAbs(dir) || !filepath.IsLocal(dir) {
			return fmt.Errorf("jellyfin: library directory %q must be a relative path inside the destination", dir)
		}
	}
	return nil
}

// Path returns the destination path of a file with identity id and
// extension ext, relative to the destination root. Each sidecar of the file
// keeps the same name with its own suffix. Path returns an error if a folder
// name, the file name, or a sidecar name is longer than MaxName bytes.
func (l Layout) Path(id ident.Identity, ext string, sidecars ...string) (string, error) {
	ext = strings.ToLower(ext)

	var dirs []string // the folders under the library directory
	var lib, name string
	switch id.Kind {
	case ident.KindMovie:
		base, err := videoBase(id)
		if err != nil {
			return "", err
		}
		lib, dirs, name = l.Movies, []string{base}, base

	case ident.KindEpisode:
		series, err := videoBase(id)
		if err != nil {
			return "", err
		}
		if id.Season < 0 || id.Episode < 1 {
			return "", fmt.Errorf("invalid episode S%02dE%02d", id.Season, id.Episode)
		}
		name = fmt.Sprintf("%s - S%02dE%02d", titleYear(id.Title, id.Year), id.Season, id.Episode)
		if t := Sanitize(id.EpisodeTitle); t != "" {
			name += " - " + t
		}
		lib, dirs = l.Shows, []string{series, fmt.Sprintf("Season %02d", id.Season)}

	case ident.KindTrack:
		artist, album, title := Sanitize(id.Artist), Sanitize(id.Album), Sanitize(id.TrackTitle)
		if artist == "" || album == "" || title == "" {
			return "", errors.New("a track needs an artist, an album, and a title")
		}
		name = title
		switch {
		case id.Track > 0 && (id.DiscTotal > 1 || id.Disc > 1):
			name = fmt.Sprintf("%d-%02d - %s", max(id.Disc, 1), id.Track, title)
		case id.Track > 0:
			name = fmt.Sprintf("%02d - %s", id.Track, title)
		}
		lib, dirs = l.Music, []string{artist, titleYear(album, id.Year)}

	default:
		return "", fmt.Errorf("unknown kind %q", id.Kind)
	}

	for _, d := range dirs {
		if len(d) > MaxName {
			return "", tooLong("a folder name", len(d))
		}
	}
	if n := len(name) + len(ext); n > MaxName {
		return "", tooLong("the file name", n)
	}
	for _, s := range sidecars {
		if n := len(name) + len(s); n > MaxName {
			return "", tooLong("the name of its "+s+" file", n)
		}
	}
	return filepath.Join(append(append([]string{lib}, dirs...), name+ext)...), nil
}

func tooLong(what string, n int) error {
	return fmt.Errorf("%s would be %d bytes, the limit is %d", what, n, MaxName)
}

// UnidentifiedPath returns the destination path of an unidentified file
// that is at rel relative to the source root. The original name stays.
func (l Layout) UnidentifiedPath(rel string) string {
	return filepath.Join(l.Unidentified, rel)
}

// SidecarPath returns the destination path of a sidecar file, given the
// destination path of its main file and the sidecar suffix (for example
// ".en.forced.srt").
func SidecarPath(mainDst, suffix string) string {
	return strings.TrimSuffix(mainDst, filepath.Ext(mainDst)) + suffix
}

// videoBase returns "Title (Year) [tmdbid-N]", the folder name of a movie or
// a series.
func videoBase(id ident.Identity) (string, error) {
	title := Sanitize(id.Title)
	if title == "" {
		return "", errors.New("a movie or series needs a title")
	}
	var tag string
	switch {
	case id.TMDBID > 0:
		tag = fmt.Sprintf(" [tmdbid-%d]", id.TMDBID)
	case id.IMDbID != "":
		tag = fmt.Sprintf(" [imdbid-%s]", id.IMDbID)
	}
	return titleYear(title, id.Year) + tag, nil
}

func titleYear(title string, year int) string {
	title = Sanitize(title)
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// Sanitize makes s safe as a path component. It uses the "smart"
// replacements of Radarr for characters that filesystems or Jellyfin do not
// permit, and removes control characters, extra whitespace, and dots and
// spaces at either end.
func Sanitize(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")

	var b strings.Builder
	for _, r := range s {
		switch r {
		case ':', '*':
			b.WriteByte('-')
		case '/', '\\':
			b.WriteByte('+')
		case '?':
			b.WriteByte('!')
		case '<', '>', '|', '"':
		default:
			if unicode.IsControl(r) {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}

	out := strings.Join(strings.Fields(b.String()), " ")
	return strings.Trim(out, ". ")
}
