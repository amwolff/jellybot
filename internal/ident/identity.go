// Package ident identifies media files with deterministic methods: file
// hashes, exact file sizes, embedded tags, and audio fingerprints. A file or
// directory name of the form "<provider>:<ID>" goes before these methods.
package ident

// Kind is the kind of an identified file.
type Kind string

// Kinds of identified files.
const (
	KindMovie   Kind = "movie"
	KindEpisode Kind = "episode"
	KindTrack   Kind = "track"
)

// Sources of identities.
const (
	SourceOpenSubtitles = "opensubtitles"
	SourceSRRDB         = "srrdb"
	SourceTags          = "tags"
	SourceAcoustID      = "acoustid"
	SourceUser          = "user"
	// SourceName is a file or directory name of the form "<provider>:<ID>".
	SourceName = "name"
)

// Identity is what a file is, with the names that its destination path
// needs.
type Identity struct {
	Kind   Kind
	Source string
	// Detail tells how jellybot found the identity, for display.
	Detail string

	// Movies and episodes. For an episode, TMDBID and IMDbID belong to
	// the series, and Title and Year are the series name and first-air
	// year.
	TMDBID       int
	IMDbID       string
	Title        string
	Year         int
	Season       int
	Episode      int
	EpisodeTitle string

	// Tracks. Artist is the album artist.
	MBRecording string
	MBRelease   string
	Artist      string
	Album       string
	TrackTitle  string
	Track       int
	Disc        int
	DiscTotal   int
}
