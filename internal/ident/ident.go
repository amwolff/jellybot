package ident

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amwolff/jellybot/internal/media"
	"github.com/amwolff/jellybot/internal/oshash"
	"github.com/amwolff/jellybot/internal/provider/acoustid"
	"github.com/amwolff/jellybot/internal/provider/musicbrainz"
	"github.com/amwolff/jellybot/internal/provider/opensubtitles"
	"github.com/amwolff/jellybot/internal/provider/srrdb"
	"github.com/amwolff/jellybot/internal/provider/tmdb"
	"github.com/amwolff/jellybot/internal/store"
)

const (
	// minAcoustIDScore is the lowest AcoustID score that jellybot accepts.
	minAcoustIDScore = 0.9
	// maxSRRResults is the most srrDB results for one size that jellybot
	// examines. More results mean that the size is too common to be
	// useful (small files, for example).
	maxSRRResults = 10
	// maxSRRDetails is the most releases whose CRC32 jellybot compares.
	maxSRRDetails = 5
)

// Stages reported through Progress.
const (
	StageHashing       = "hashing"
	StageOpenSubtitles = "looking up (OpenSubtitles)"
	StageSRRDB         = "looking up (srrDB)"
	StageCRC           = "verifying CRC32"
	StageTMDB          = "looking up (TMDB)"
	StageTags          = "reading tags"
	StageFingerprint   = "fingerprinting"
	StageAcoustID      = "looking up (AcoustID)"
	StageMusicBrainz   = "looking up (MusicBrainz)"
)

// File is a file to identify.
type File struct {
	Path    string
	Size    int64
	ModTime time.Time
	Kind    media.Kind
}

// Result is the outcome of an identification. Exactly one of Identity,
// Candidates, and Reason is set.
type Result struct {
	Identity *Identity
	// Candidates holds the identities of an ambiguous result.
	Candidates []Identity
	// Reason tells why the file is unmatched.
	Reason string
	// Key identifies the file contents. It is valid when HasKey is set.
	Key    store.IdentKey
	HasKey bool
}

// Identifier identifies files. A nil client disables its provider.
type Identifier struct {
	Store         *store.Store
	OpenSubtitles *opensubtitles.Client
	SRRDB         *srrdb.Client
	TMDB          *tmdb.Client
	AcoustID      *acoustid.Client
	MusicBrainz   *musicbrainz.Client
	// FPCalc is the path of the fpcalc binary, or empty.
	FPCalc string
	Log    *slog.Logger
}

// Identify identifies f. It calls progress with each stage. Errors are
// failures (for example, of the network); a file that jellybot cannot
// identify gives a Result with a Reason instead.
func (x *Identifier) Identify(ctx context.Context, f File, progress func(stage string)) (Result, error) {
	if progress == nil {
		progress = func(string) {}
	}
	switch f.Kind {
	case media.Video:
		return x.video(ctx, f, progress)
	case media.Audio:
		return x.audio(ctx, f, progress)
	}
	return Result{Reason: "not a media file"}, nil
}

// Choose records id as the identity of the file contents with key k.
func (x *Identifier) Choose(k store.IdentKey, id Identity) error {
	return x.Store.PutIdent(k, record(id, SourceUser))
}

func record(id Identity, source string) store.IdentRecord {
	return store.IdentRecord{
		Kind:        string(id.Kind),
		TMDBID:      id.TMDBID,
		IMDbID:      id.IMDbID,
		Season:      id.Season,
		Episode:     id.Episode,
		MBRecording: id.MBRecording,
		MBRelease:   id.MBRelease,
		Source:      source,
	}
}

func (x *Identifier) log() *slog.Logger {
	if x.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return x.Log
}

// stat returns the hashes of f, from the memo if f did not change.
func (x *Identifier) stat(f File) (store.StatEntry, error) {
	e, ok, err := x.Store.Stat(f.Path)
	if err != nil {
		return store.StatEntry{}, err
	}
	if ok && e.Size == f.Size && e.MtimeNs == f.ModTime.UnixNano() {
		return e, nil
	}

	h, size, err := oshash.File(f.Path)
	if err != nil {
		return store.StatEntry{}, err
	}
	e = store.StatEntry{Size: size, MtimeNs: f.ModTime.UnixNano(), OSHash: uint64(h)}
	return e, x.Store.PutStat(f.Path, e)
}

// crc returns the CRC32 of the whole file, from the memo if possible.
func (x *Identifier) crc(ctx context.Context, f File, e *store.StatEntry) (uint32, error) {
	if e.CRC32 != nil {
		return *e.CRC32, nil
	}

	fh, err := os.Open(f.Path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = fh.Close() }()

	h := crc32.NewIEEE()
	if _, err := io.CopyBuffer(h, ctxReader{ctx: ctx, r: fh}, make([]byte, 1<<20)); err != nil {
		return 0, err
	}
	sum := h.Sum32()
	e.CRC32 = &sum
	return sum, x.Store.PutStat(f.Path, *e)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (x *Identifier) video(ctx context.Context, f File, progress func(string)) (Result, error) {
	if x.TMDB == nil {
		return Result{Reason: "no TMDB key (TMDB gives the names of videos)"}, nil
	}

	progress(StageHashing)
	st, err := x.stat(f)
	if errors.Is(err, oshash.ErrTooSmall) {
		return Result{Reason: "file is smaller than 128 KiB"}, nil
	}
	if err != nil {
		return Result{}, err
	}
	res := Result{Key: store.IdentKey{Size: st.Size, OSHash: st.OSHash}, HasKey: true}

	if !store.IsRefresh(ctx) {
		rec, ok, err := x.Store.Ident(res.Key)
		if err != nil {
			return Result{}, err
		}
		if ok {
			progress(StageTMDB)
			id, reason, err := x.resolveVideo(ctx, Identity{
				Kind: Kind(rec.Kind), Source: rec.Source, TMDBID: rec.TMDBID, IMDbID: rec.IMDbID,
				Season: rec.Season, Episode: rec.Episode,
				Detail: fmt.Sprintf("recorded identity (%s, %s)", rec.Source, oshash.Hash(st.OSHash)),
			})
			if err != nil {
				return Result{}, err
			}
			if reason == "" {
				res.Identity = &id
				return res, nil
			}
			x.log().DebugContext(ctx, "recorded identity no longer resolves", "path", f.Path, "reason", reason)
		}
	}

	var reasons []string

	if x.OpenSubtitles != nil {
		progress(StageOpenSubtitles)
		r, ok, err := x.viaOpenSubtitles(ctx, st, res, progress)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return r, nil
		}
		reasons = append(reasons, "OpenSubtitles: "+r.Reason)
	} else {
		reasons = append(reasons, "no OpenSubtitles key")
	}

	if x.SRRDB != nil {
		progress(StageSRRDB)
		r, ok, err := x.viaSRRDB(ctx, f, &st, res, progress)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return r, nil
		}
		reasons = append(reasons, "srrDB: "+r.Reason)
	}

	res.Reason = strings.Join(reasons, "; ")
	return res, nil
}

func (x *Identifier) viaOpenSubtitles(ctx context.Context, st store.StatEntry, res Result, progress func(string)) (Result, bool, error) {
	h := oshash.Hash(st.OSHash)
	features, err := x.OpenSubtitles.SearchHash(ctx, h)
	if err != nil {
		return Result{}, false, err
	}

	var cands []Identity
	seen := make(map[string]bool)
	for _, ft := range features {
		var id Identity
		switch ft.Type {
		case opensubtitles.TypeMovie:
			id = Identity{Kind: KindMovie, TMDBID: ft.TMDBID, IMDbID: opensubtitles.IMDbTag(ft.IMDbID), Title: ft.Title, Year: ft.Year}
		case opensubtitles.TypeEpisode:
			if ft.Episode < 1 {
				continue
			}
			id = Identity{Kind: KindEpisode, TMDBID: ft.ParentTMDBID, IMDbID: opensubtitles.IMDbTag(ft.ParentIMDbID),
				Title: ft.ParentTitle, Season: ft.Season, Episode: ft.Episode}
		default:
			continue
		}
		if id.TMDBID == 0 && id.IMDbID == "" {
			continue
		}
		k := fmt.Sprintf("%s|%d|%s|%d|%d", id.Kind, id.TMDBID, id.IMDbID, id.Season, id.Episode)
		if seen[k] {
			continue
		}
		seen[k] = true
		id.Source = SourceOpenSubtitles
		id.Detail = fmt.Sprintf("OpenSubtitles hash %s", h)
		cands = append(cands, id)
	}

	if len(cands) == 0 {
		res.Reason = "no match"
		return res, false, nil
	}

	progress(StageTMDB)
	var resolved []Identity
	var reasons []string
	for _, c := range cands {
		id, reason, err := x.resolveVideo(ctx, c)
		if err != nil {
			return Result{}, false, err
		}
		if reason != "" {
			reasons = append(reasons, reason)
			continue
		}
		resolved = append(resolved, id)
	}

	switch len(resolved) {
	case 0:
		res.Reason = strings.Join(reasons, "; ")
		return res, false, nil
	case 1:
		res.Identity = &resolved[0]
		return res, true, x.Store.PutIdent(res.Key, record(resolved[0], SourceOpenSubtitles))
	default:
		res.Candidates = resolved
		return res, true, nil
	}
}

var episodeTag = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])S(\d{1,3})E(\d{1,4})(?:[^0-9]|$)`)

func (x *Identifier) viaSRRDB(ctx context.Context, f File, st *store.StatEntry, res Result, progress func(string)) (Result, bool, error) {
	releases, total, err := x.SRRDB.SearchSize(ctx, f.Size)
	if err != nil {
		return Result{}, false, err
	}
	if len(releases) == 0 {
		res.Reason = "no match"
		return res, false, nil
	}
	if total > maxSRRResults {
		res.Reason = fmt.Sprintf("%d releases have a file of this size", total)
		return res, false, nil
	}

	progress(StageCRC)
	sum, err := x.crc(ctx, f, st)
	if err != nil {
		return Result{}, false, err
	}

	progress(StageSRRDB)
	var match *srrdb.Release
	for i, rel := range releases[:min(len(releases), maxSRRDetails)] {
		files, ferr := x.SRRDB.ArchivedFiles(ctx, rel.Name)
		if ferr != nil {
			return Result{}, false, ferr
		}
		if slices.ContainsFunc(files, func(af srrdb.ArchivedFile) bool { return af.Size == f.Size && af.CRC == sum }) {
			match = &releases[i]
			break
		}
	}
	if match == nil {
		res.Reason = fmt.Sprintf("no release with CRC32 %08X", sum)
		return res, false, nil
	}
	if match.IMDbID == "" {
		res.Reason = fmt.Sprintf("release %s has no IMDb ID", match.Name)
		return res, false, nil
	}

	progress(StageTMDB)
	found, err := x.TMDB.FindIMDb(ctx, match.IMDbID)
	if err != nil {
		return Result{}, false, err
	}
	detail := fmt.Sprintf("srrDB %s (size + CRC32 %08X)", match.Name, sum)
	var id Identity
	switch {
	case len(found.Movies) > 0:
		id = Identity{Kind: KindMovie, TMDBID: found.Movies[0].ID, IMDbID: match.IMDbID}
	case len(found.Episodes) > 0:
		e := found.Episodes[0]
		id = Identity{Kind: KindEpisode, TMDBID: e.ShowID, Season: e.SeasonNumber, Episode: e.EpisodeNumber}
	case len(found.Series) > 0:
		m := episodeTag.FindStringSubmatch(match.Name)
		if m == nil {
			res.Reason = fmt.Sprintf("release %s is a TV release without SxxEyy", match.Name)
			return res, false, nil
		}
		season, _ := strconv.Atoi(m[1])
		episode, _ := strconv.Atoi(m[2])
		id = Identity{Kind: KindEpisode, TMDBID: found.Series[0].ID, IMDbID: match.IMDbID, Season: season, Episode: episode}
	default:
		res.Reason = fmt.Sprintf("TMDB does not know %s", match.IMDbID)
		return res, false, nil
	}
	id.Source = SourceSRRDB
	id.Detail = detail

	id, reason, err := x.resolveVideo(ctx, id)
	if err != nil {
		return Result{}, false, err
	}
	if reason != "" {
		res.Reason = reason
		return res, false, nil
	}
	res.Identity = &id
	return res, true, x.Store.PutIdent(res.Key, record(id, SourceSRRDB))
}

// resolveVideo fills in the TMDB names of a movie or an episode. A non-empty
// reason means that TMDB does not know the identity.
func (x *Identifier) resolveVideo(ctx context.Context, id Identity) (Identity, string, error) {
	if id.TMDBID == 0 && id.IMDbID != "" {
		found, err := x.TMDB.FindIMDb(ctx, id.IMDbID)
		if err != nil {
			return id, "", err
		}
		switch {
		case id.Kind == KindMovie && len(found.Movies) > 0:
			id.TMDBID = found.Movies[0].ID
		case id.Kind == KindEpisode && len(found.Series) > 0:
			id.TMDBID = found.Series[0].ID
		case id.Kind == KindEpisode && len(found.Episodes) > 0:
			id.TMDBID = found.Episodes[0].ShowID
		}
	}
	if id.TMDBID == 0 {
		return id, fmt.Sprintf("TMDB does not know %s", cmp.Or(id.IMDbID, "the "+string(id.Kind))), nil
	}

	switch id.Kind {
	case KindMovie:
		m, ok, err := x.TMDB.Movie(ctx, id.TMDBID)
		if err != nil {
			return id, "", err
		}
		if !ok {
			return id, fmt.Sprintf("TMDB has no movie %d", id.TMDBID), nil
		}
		id.Title, id.Year = m.Title, m.Year()
		id.IMDbID = cmp.Or(m.IMDbID, id.IMDbID)
		id.Detail += fmt.Sprintf(" → tmdb %d", id.TMDBID)
		return id, "", nil

	case KindEpisode:
		s, ok, err := x.TMDB.Series(ctx, id.TMDBID)
		if err != nil {
			return id, "", err
		}
		if !ok {
			return id, fmt.Sprintf("TMDB has no series %d", id.TMDBID), nil
		}
		id.Title, id.Year = s.Name, s.Year()
		e, ok, err := x.TMDB.Episode(ctx, id.TMDBID, id.Season, id.Episode)
		if err != nil {
			return id, "", err
		}
		if ok {
			id.EpisodeTitle = e.Name
		}
		id.Detail += fmt.Sprintf(" → tmdb %d S%02dE%02d", id.TMDBID, id.Season, id.Episode)
		return id, "", nil
	}

	return id, fmt.Sprintf("unexpected kind %q", id.Kind), nil
}

func (x *Identifier) audio(ctx context.Context, f File, progress func(string)) (Result, error) {
	progress(StageTags)
	tags, err := media.ReadTags(f.Path)
	if err != nil {
		x.log().DebugContext(ctx, "cannot read tags", "path", f.Path, "error", err)
	}
	if err == nil && tags.Complete() {
		return Result{Identity: &Identity{
			Kind:        KindTrack,
			Source:      SourceTags,
			Detail:      "embedded tags",
			MBRecording: tags.MBRecording,
			MBRelease:   tags.MBRelease,
			Artist:      cmp.Or(tags.AlbumArtist, tags.Artist),
			Album:       tags.Album,
			TrackTitle:  tags.Title,
			Track:       tags.Track,
			Disc:        tags.Disc,
			DiscTotal:   tags.DiscTotal,
			Year:        tags.Year,
		}}, nil
	}

	if x.FPCalc == "" {
		return Result{Reason: "incomplete tags; install fpcalc for fingerprints"}, nil
	}
	if x.AcoustID == nil {
		return Result{Reason: "incomplete tags; no AcoustID key"}, nil
	}

	var res Result
	progress(StageHashing)
	st, err := x.stat(f)
	switch {
	case err == nil:
		res.Key, res.HasKey = store.IdentKey{Size: st.Size, OSHash: st.OSHash}, true
	case !errors.Is(err, oshash.ErrTooSmall):
		return Result{}, err
	}

	if res.HasKey && !store.IsRefresh(ctx) {
		rec, ok, ierr := x.Store.Ident(res.Key)
		if ierr != nil {
			return Result{}, ierr
		}
		if ok && rec.MBRecording != "" {
			progress(StageMusicBrainz)
			id, reason, rerr := x.resolveTrack(ctx, rec.MBRecording, rec.MBRelease, tags.Album)
			if rerr != nil {
				return Result{}, rerr
			}
			if reason == "" {
				id.Source = rec.Source
				id.Detail = fmt.Sprintf("recorded identity (%s) → MusicBrainz %s", rec.Source, rec.MBRecording)
				res.Identity = &id
				return res, nil
			}
		}
	}

	progress(StageFingerprint)
	fp, duration, err := media.Fingerprint(ctx, x.FPCalc, f.Path)
	if err != nil {
		return Result{}, err
	}

	progress(StageAcoustID)
	results, err := x.AcoustID.Lookup(ctx, fp, duration)
	if err != nil {
		return Result{}, err
	}
	var best *acoustid.Result
	for i, r := range results {
		if r.Score >= minAcoustIDScore && len(r.Recordings) > 0 {
			best = &results[i]
			break
		}
	}
	if best == nil {
		res.Reason = "AcoustID: no match"
		return res, nil
	}

	progress(StageMusicBrainz)
	reason := "MusicBrainz: no release"
	for _, recID := range best.Recordings {
		id, r, err := x.resolveTrack(ctx, recID, "", tags.Album)
		if err != nil {
			return Result{}, err
		}
		if r != "" {
			reason = r
			continue
		}
		id.Source = SourceAcoustID
		id.Detail = fmt.Sprintf("AcoustID %.0f%% → MusicBrainz %s", best.Score*100, recID)
		res.Identity = &id
		if res.HasKey {
			return res, x.Store.PutIdent(res.Key, record(id, SourceAcoustID))
		}
		return res, nil
	}
	res.Reason = reason
	return res, nil
}

// resolveTrack fills in the MusicBrainz names of a recording on a release.
// If releaseID is empty, it selects a release.
func (x *Identifier) resolveTrack(ctx context.Context, recordingID, releaseID, albumHint string) (Identity, string, error) {
	if x.MusicBrainz == nil {
		return Identity{}, "MusicBrainz is not available", nil
	}
	rec, ok, err := x.MusicBrainz.Recording(ctx, recordingID)
	if err != nil {
		return Identity{}, "", err
	}
	if !ok {
		return Identity{}, fmt.Sprintf("MusicBrainz has no recording %s", recordingID), nil
	}

	var rel *musicbrainz.Release
	if releaseID != "" {
		for i := range rec.Releases {
			if rec.Releases[i].ID == releaseID && hasTrack(rec.Releases[i]) {
				rel = &rec.Releases[i]
			}
		}
	}
	if rel == nil {
		r, ok := ChooseRelease(rec.Releases, albumHint)
		if !ok {
			return Identity{}, fmt.Sprintf("MusicBrainz has no release of recording %s", recordingID), nil
		}
		rel = &r
	}

	discs, err := x.MusicBrainz.MediumCount(ctx, rel.ID)
	if err != nil {
		return Identity{}, "", err
	}
	medium := rel.Media[0]
	track := medium.Tracks[0]
	return Identity{
		Kind:        KindTrack,
		MBRecording: rec.ID,
		MBRelease:   rel.ID,
		Artist:      cmp.Or(rel.Artist, rec.Artist),
		Album:       rel.Title,
		TrackTitle:  cmp.Or(track.Title, rec.Title),
		Track:       track.Position,
		Disc:        medium.Position,
		DiscTotal:   max(discs, 1),
		Year:        year(rel.Date),
	}, "", nil
}

func hasTrack(r musicbrainz.Release) bool {
	return len(r.Media) > 0 && len(r.Media[0].Tracks) > 0
}

// ChooseRelease selects one release in this sequence:
//  1. a release whose title is albumHint (if it is set);
//  2. an official album without secondary types (not a compilation, for
//     example), then an official album, then an official release, then
//     any release;
//  3. the earliest date (releases without a date come last);
//  4. the lowest MBID.
func ChooseRelease(releases []musicbrainz.Release, albumHint string) (musicbrainz.Release, bool) {
	var cands []musicbrainz.Release
	for _, r := range releases {
		if hasTrack(r) {
			cands = append(cands, r)
		}
	}
	if albumHint != "" {
		var named []musicbrainz.Release
		for _, r := range cands {
			if strings.EqualFold(r.Title, albumHint) {
				named = append(named, r)
			}
		}
		if len(named) > 0 {
			cands = named
		}
	}
	if len(cands) == 0 {
		return musicbrainz.Release{}, false
	}

	rank := func(r musicbrainz.Release) int {
		official := r.Status == "Official"
		album := r.PrimaryType == "Album"
		switch {
		case official && album && len(r.SecondaryTypes) == 0:
			return 0
		case official && album:
			return 1
		case official:
			return 2
		}
		return 3
	}
	slices.SortStableFunc(cands, func(a, b musicbrainz.Release) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		if (a.Date == "") != (b.Date == "") {
			if a.Date == "" {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(a.Date, b.Date); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return cands[0], true
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
