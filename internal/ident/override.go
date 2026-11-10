package ident

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amwolff/jellybot/internal/media"
	"github.com/amwolff/jellybot/internal/oshash"
	"github.com/amwolff/jellybot/internal/provider/musicbrainz"
	"github.com/amwolff/jellybot/internal/store"
)

const (
	providerTMDB        = "tmdb"
	providerIMDb        = "imdb"
	providerMusicBrainz = "musicbrainz"
)

// maxLengthDiff is the most that the length of an audio file can differ from
// the length of the track that its position in an album gives it.
const maxLengthDiff = 10 * time.Second

// override is an identity that the user gives in a name of the form
// "<provider>:<ID>". The name is that of a file, without its extension, or
// that of a directory of audio files.
type override struct {
	provider string
	// id is a TMDB ID, an IMDb ID ("tt…"), or an MBID.
	id string
	// season and episode are valid if hasEpisode is set.
	hasEpisode      bool
	season, episode int
}

func (o override) String() string {
	s := o.provider + ":" + o.id
	if o.hasEpisode {
		s += fmt.Sprintf(":s%02de%02d", o.season, o.episode)
	}
	return s
}

var (
	tmdbIDPattern  = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	imdbIDPattern  = regexp.MustCompile(`^tt[0-9]+$`)
	mbidPattern    = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	episodePattern = regexp.MustCompile(`^s([0-9]{1,3})e([0-9]{1,4})$`)
)

// parseOverride parses name, without regard to case. ok reports whether name
// starts with a provider; err is then set if the rest is not valid.
func parseOverride(name string) (o override, ok bool, err error) {
	provider, rest, found := strings.Cut(strings.ToLower(strings.TrimSpace(name)), ":")
	if !found {
		return override{}, false, nil
	}

	var idPattern *regexp.Regexp
	var form string
	switch provider {
	case providerTMDB:
		idPattern, form = tmdbIDPattern, "tmdb:<ID> or tmdb:<ID>:s<season>e<episode>"
	case providerIMDb:
		idPattern, form = imdbIDPattern, "imdb:<tt ID> or imdb:<tt ID>:s<season>e<episode>"
	case providerMusicBrainz:
		idPattern, form = mbidPattern, "musicbrainz:<MBID>"
	default:
		return override{}, false, nil
	}
	o.provider = provider
	err = errors.New("not " + form)

	id, episode, hasEpisode := strings.Cut(rest, ":")
	if !idPattern.MatchString(id) {
		return o, true, err
	}
	o.id = id
	if hasEpisode {
		m := episodePattern.FindStringSubmatch(episode)
		if m == nil || provider == providerMusicBrainz {
			return o, true, err
		}
		o.hasEpisode = true
		o.season, _ = strconv.Atoi(m[1])
		o.episode, _ = strconv.Atoi(m[2])
	}
	return o, true, nil
}

// stem returns the name of the file at path without its extension.
func stem(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// videoOverride identifies a video whose name is o. res holds the key of the
// file, if it has one.
func (x *Identifier) videoOverride(ctx context.Context, o override, res Result, progress func(string)) (Result, error) {
	id := Identity{Kind: KindMovie, Source: SourceName, Detail: "file name " + o.String()}
	if o.hasEpisode {
		id.Kind, id.Season, id.Episode = KindEpisode, o.season, o.episode
	}

	progress(StageTMDB)
	switch o.provider {
	case providerTMDB:
		id.TMDBID, _ = strconv.Atoi(o.id)
	case providerIMDb:
		found, err := x.TMDB.FindIMDb(ctx, o.id)
		if err != nil {
			return Result{}, err
		}
		switch {
		case o.hasEpisode && len(found.Series) > 0:
			id.TMDBID, id.IMDbID = found.Series[0].ID, o.id
		case o.hasEpisode:
			res.Reason = fmt.Sprintf("TMDB has no series %s", o.id)
		case len(found.Movies) > 0:
			id.TMDBID, id.IMDbID = found.Movies[0].ID, o.id
		case len(found.Episodes) > 0:
			e := found.Episodes[0]
			id.Kind, id.TMDBID, id.Season, id.Episode = KindEpisode, e.ShowID, e.SeasonNumber, e.EpisodeNumber
		case len(found.Series) > 0:
			res.Reason = fmt.Sprintf("%s is a series; add the episode, as in imdb:%s:s01e02", o.id, o.id)
		default:
			res.Reason = fmt.Sprintf("TMDB does not know %s", o.id)
		}
	default:
		res.Reason = fmt.Sprintf("a %s name is for an audio file", o.provider)
	}
	if res.Reason != "" {
		return res, nil
	}

	id, reason, err := x.resolveVideo(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if reason == "" && id.Kind == KindEpisode {
		// A typed number can be wrong, so the episode must exist.
		_, ok, err := x.TMDB.Episode(ctx, id.TMDBID, id.Season, id.Episode)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			reason = fmt.Sprintf("TMDB has no S%02dE%02d of series %d", id.Season, id.Episode, id.TMDBID)
		}
	}
	if reason != "" {
		res.Reason = reason
		return res, nil
	}

	res.Identity = &id
	if res.HasKey {
		return res, x.Store.PutIdent(res.Key, record(id, SourceName))
	}
	return res, nil
}

// audioOverride identifies an audio file from its name, which gives a
// recording, or from the name of the nearest directory above it that gives a
// release. ok is false if neither name is an override.
func (x *Identifier) audioOverride(ctx context.Context, f File, progress func(string)) (res Result, ok bool, err error) {
	o, isFile, fileErr := parseOverride(stem(f.Path))
	dir, album, isAlbum, albumErr := albumOverride(filepath.Dir(f.Path))
	switch {
	case !isFile && !isAlbum:
		return Result{}, false, nil
	case isFile && fileErr != nil:
		return Result{Reason: "the file name is " + fileErr.Error()}, true, nil
	case isFile && o.provider != providerMusicBrainz:
		return Result{Reason: fmt.Sprintf("a %s name is for a video", o.provider)}, true, nil
	case !isFile && albumErr != nil:
		return Result{Reason: "the folder name is " + albumErr.Error()}, true, nil
	}

	progress(StageHashing)
	st, err := x.stat(f)
	switch {
	case err == nil:
		res.Key, res.HasKey = store.IdentKey{Size: st.Size, OSHash: st.OSHash}, true
	case !errors.Is(err, oshash.ErrTooSmall):
		return Result{}, true, err
	}

	progress(StageMusicBrainz)
	var id Identity
	var reason string
	switch {
	case !isFile:
		id, reason, err = x.trackInAlbum(ctx, dir, album, f.Path)
	case isAlbum && albumErr == nil:
		id, reason, err = x.releaseTrack(ctx, album.id, o.id)
		id.Detail = fmt.Sprintf("file name %s, folder name %s", o, album)
	default:
		// Without readable tags, there is no album hint.
		tags, _ := media.ReadTags(f.Path)
		id, reason, err = x.resolveTrack(ctx, o.id, "", tags.Album)
		id.Source, id.Detail = SourceName, "file name "+o.String()
	}
	if err != nil {
		return Result{}, true, err
	}
	if reason != "" {
		res.Reason = reason
		return res, true, nil
	}

	res.Identity = &id
	if res.HasKey {
		return res, true, x.Store.PutIdent(res.Key, record(id, SourceName))
	}
	return res, true, nil
}

// albumOverride returns the nearest directory at or above dir whose name is
// a MusicBrainz override.
func albumOverride(dir string) (string, override, bool, error) {
	for {
		o, ok, err := parseOverride(filepath.Base(dir))
		if ok && o.provider == providerMusicBrainz {
			return dir, o, true, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", override{}, false, nil
		}
		dir = parent
	}
}

// release returns the release with MBID id and all its tracks.
func (x *Identifier) release(ctx context.Context, id string) (musicbrainz.Release, string, error) {
	if x.MusicBrainz == nil {
		return musicbrainz.Release{}, "MusicBrainz is not available", nil
	}
	rel, ok, err := x.MusicBrainz.Release(ctx, id)
	if err != nil {
		return musicbrainz.Release{}, "", err
	}
	if !ok {
		return musicbrainz.Release{}, fmt.Sprintf("MusicBrainz has no release %s", id), nil
	}
	return rel, "", nil
}

type albumTrack struct {
	disc int
	musicbrainz.Track
}

func albumTracks(rel musicbrainz.Release) []albumTrack {
	var tracks []albumTrack
	for _, m := range rel.Media {
		for _, t := range m.Tracks {
			tracks = append(tracks, albumTrack{disc: m.Position, Track: t})
		}
	}
	return tracks
}

func trackIdentity(rel musicbrainz.Release, t albumTrack) Identity {
	return Identity{
		Kind:        KindTrack,
		Source:      SourceName,
		MBRecording: t.RecordingID,
		MBRelease:   rel.ID,
		Artist:      rel.Artist,
		Album:       rel.Title,
		TrackTitle:  t.Title,
		Track:       t.Position,
		Disc:        t.disc,
		DiscTotal:   max(len(rel.Media), 1),
		Year:        year(rel.Date),
	}
}

// releaseTrack returns the first track of a release that is the recording.
func (x *Identifier) releaseTrack(ctx context.Context, releaseID, recordingID string) (Identity, string, error) {
	rel, reason, err := x.release(ctx, releaseID)
	if err != nil || reason != "" {
		return Identity{}, reason, err
	}
	for _, t := range albumTracks(rel) {
		if t.RecordingID == recordingID {
			return trackIdentity(rel, t), "", nil
		}
	}
	return Identity{}, fmt.Sprintf("MusicBrainz release %s does not have recording %s", releaseID, recordingID), nil
}

// trackInAlbum gives the audio file at path a track of the release that the
// name of dir gives. A file below dir whose own name gives a recording takes
// the track of that recording. The other files take the other tracks in
// order, and each must be as long as its track.
func (x *Identifier) trackInAlbum(ctx context.Context, dir string, album override, path string) (Identity, string, error) {
	rel, reason, err := x.release(ctx, album.id)
	if err != nil || reason != "" {
		return Identity{}, reason, err
	}
	files, err := albumFiles(dir)
	if err != nil {
		return Identity{}, "", err
	}

	tracks := albumTracks(rel)
	taken := make([]bool, len(tracks))
	var inOrder []string
	for _, name := range files {
		o, ok, _ := parseOverride(stem(name))
		if !ok {
			inOrder = append(inOrder, name)
			continue
		}
		for i, t := range tracks {
			if !taken[i] && o.id != "" && t.RecordingID == o.id {
				taken[i] = true
				break
			}
		}
	}
	var free []albumTrack
	for i, t := range tracks {
		if !taken[i] {
			free = append(free, t)
		}
	}

	name, err := filepath.Rel(dir, path)
	if err != nil {
		return Identity{}, "", err
	}
	pos := slices.Index(inOrder, name)
	if pos < 0 {
		return Identity{}, "", fmt.Errorf("ident: %s: %w", path, fs.ErrNotExist)
	}
	if len(inOrder) != len(free) {
		return Identity{}, fmt.Sprintf("the folder has %d audio files for %d tracks", len(inOrder), len(free)), nil
	}
	t := free[pos]
	// A length that the file or MusicBrainz does not give is not examined.
	if d, err := media.Duration(path); err == nil && d > 0 && t.Length > 0 && (d-t.Length).Abs() > maxLengthDiff {
		return Identity{}, fmt.Sprintf("file %d of %d is %s long, but its track is %s long",
			pos+1, len(inOrder), clock(d), clock(t.Length)), nil
	}

	id := trackIdentity(rel, t)
	id.Detail = fmt.Sprintf("folder name %s, file %d of %d", album, pos+1, len(inOrder))
	return id, "", nil
}

// albumFiles returns the audio files below dir in the order of comparePaths,
// relative to dir. It ignores hidden names, and directories whose names give
// other releases.
func albumFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not stop the identification.
			if path != dir && d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return err
		}
		if path == dir {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if o, ok, _ := parseOverride(name); strings.HasPrefix(name, ".") || ok && o.provider == providerMusicBrainz {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !d.Type().IsRegular() || media.Classify(name) != media.Audio {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	slices.SortFunc(files, comparePaths)
	return files, err
}

// comparePaths orders paths directory by directory, and names in natural
// order.
func comparePaths(a, b string) int {
	as, bs := strings.Split(a, string(filepath.Separator)), strings.Split(b, string(filepath.Separator))
	for i := range min(len(as), len(bs)) {
		if c := compareNatural(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// compareNatural orders names as a person does: a run of digits is a number
// ("2" is before "10"), and the case of letters matters only between names
// that are otherwise the same.
func compareNatural(a, b string) int {
	x, y := strings.ToLower(a), strings.ToLower(b)
	for x != "" && y != "" {
		if isDigit(x[0]) && isDigit(y[0]) {
			nx, ny := digits(x), digits(y)
			dx, dy := strings.TrimLeft(x[:nx], "0"), strings.TrimLeft(y[:ny], "0")
			if c := cmp.Or(cmp.Compare(len(dx), len(dy)), cmp.Compare(dx, dy)); c != 0 {
				return c
			}
			x, y = x[nx:], y[ny:]
			continue
		}
		if x[0] != y[0] {
			return cmp.Compare(x[0], y[0])
		}
		x, y = x[1:], y[1:]
	}
	return cmp.Or(cmp.Compare(len(x), len(y)), cmp.Compare(a, b))
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// digits returns the length of the run of digits at the start of s.
func digits(s string) int {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	return n
}

// clock formats d as minutes and seconds.
func clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
