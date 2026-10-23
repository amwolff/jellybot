// Package jellyfin builds destination paths that follow the Jellyfin naming
// conventions.
package jellyfin

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/amwolff/jellybot/internal/ident"
)

// maxBase is the byte limit for a name before its suffix. Filesystems
// permit 255 bytes per component; the rest leaves room for long sidecar
// suffixes such as ".English Commentary.en.forced.srt".
const maxBase = 200

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
// extension ext, relative to the destination root.
func (l Layout) Path(id ident.Identity, ext string) (string, error) {
	ext = strings.ToLower(ext)

	switch id.Kind {
	case ident.KindMovie:
		base, err := videoBase(id)
		if err != nil {
			return "", err
		}
		return filepath.Join(l.Movies, base, base+ext), nil

	case ident.KindEpisode:
		series, err := videoBase(id)
		if err != nil {
			return "", err
		}
		if id.Season < 0 || id.Episode < 1 {
			return "", fmt.Errorf("jellyfin: invalid episode S%02dE%02d", id.Season, id.Episode)
		}
		name := fmt.Sprintf("%s - S%02dE%02d", titleYear(id.Title, id.Year), id.Season, id.Episode)
		if t := Sanitize(id.EpisodeTitle); t != "" {
			name += " - " + t
		}
		season := fmt.Sprintf("Season %02d", id.Season)
		return filepath.Join(l.Shows, series, season, fit(name)+ext), nil

	case ident.KindTrack:
		artist, album, title := Sanitize(id.Artist), Sanitize(id.Album), Sanitize(id.TrackTitle)
		if artist == "" || album == "" || title == "" {
			return "", errors.New("jellyfin: a track needs an artist, an album, and a title")
		}
		name := title
		switch {
		case id.Track > 0 && (id.DiscTotal > 1 || id.Disc > 1):
			name = fmt.Sprintf("%d-%02d - %s", max(id.Disc, 1), id.Track, title)
		case id.Track > 0:
			name = fmt.Sprintf("%02d - %s", id.Track, title)
		}
		return filepath.Join(l.Music, fit(artist), fit(titleYear(album, id.Year)), fit(name)+ext), nil
	}

	return "", fmt.Errorf("jellyfin: unknown kind %q", id.Kind)
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
		return "", errors.New("jellyfin: a movie or series needs a title")
	}
	var tag string
	switch {
	case id.TMDBID > 0:
		tag = fmt.Sprintf(" [tmdbid-%d]", id.TMDBID)
	case id.IMDbID != "":
		tag = fmt.Sprintf(" [imdbid-%s]", id.IMDbID)
	}
	return cut(titleYear(title, id.Year), maxBase-len(tag)) + tag, nil
}

func titleYear(title string, year int) string {
	title = Sanitize(title)
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// fit cuts s to maxBase bytes without splitting a character.
func fit(s string) string {
	return cut(s, maxBase)
}

// cut cuts s to n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimRight(s, ". ")
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
	return strings.Trim(fit(out), ". ")
}
