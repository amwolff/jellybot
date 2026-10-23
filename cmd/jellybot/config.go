package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zeebo/clingy"

	"github.com/amwolff/jellybot/internal/engine"
	"github.com/amwolff/jellybot/internal/fsop"
	"github.com/amwolff/jellybot/internal/ident"
	"github.com/amwolff/jellybot/internal/jellyfin"
	"github.com/amwolff/jellybot/internal/provider"
	"github.com/amwolff/jellybot/internal/provider/acoustid"
	"github.com/amwolff/jellybot/internal/provider/musicbrainz"
	"github.com/amwolff/jellybot/internal/provider/opensubtitles"
	"github.com/amwolff/jellybot/internal/provider/srrdb"
	"github.com/amwolff/jellybot/internal/provider/tmdb"
	"github.com/amwolff/jellybot/internal/store"
)

// config holds the flags and arguments that both commands share.
type config struct {
	src, dst    string
	move        bool
	recursive   bool
	modes       fsop.Modes
	owner       fsop.Owner
	jobs        int
	db          string
	logFile     string
	tmdbKey     string
	osKey       string
	acoustidKey string
	layout      jellyfin.Layout
}

func boolFlag(params clingy.Parameters, name, desc string, opts ...clingy.Option) bool {
	opts = append(opts, clingy.Transform(strconv.ParseBool), clingy.Boolean)
	return params.Flag(name, desc, false, opts...).(bool)
}

func (c *config) setup(params clingy.Parameters) {
	c.move = boolFlag(params, "move", "start in move mode instead of copy mode")
	c.recursive = boolFlag(params, "recursive", "scan the subdirectories of the source", clingy.Short('r'))
	c.modes = params.Flag("chmod", "octal mode overrides, for example D0700,F0600", nil,
		clingy.Transform(fsop.ParseModes)).(fsop.Modes)
	c.owner = params.Flag("chown", "owner override as numeric UID:GID, for example 1000:1000", nil,
		clingy.Transform(fsop.ParseOwner)).(fsop.Owner)
	c.jobs = params.Flag("jobs", "number of files to identify at the same time", 4,
		clingy.Transform(strconv.Atoi)).(int)
	c.db = params.Flag("db", "path of the jellybot database", store.DefaultPath()).(string)
	c.logFile = params.Flag("log-file", "write the log of the TUI to this file", "").(string)
	c.tmdbKey = params.Flag("tmdb-api-key", "TMDB API key or API read access token", "", clingy.Advanced).(string)
	c.osKey = params.Flag("opensubtitles-api-key", "OpenSubtitles API key", "", clingy.Advanced).(string)
	c.acoustidKey = params.Flag("acoustid-api-key", "AcoustID application API key", "", clingy.Advanced).(string)
	c.layout.Movies = params.Flag("movies-dir", "movie library directory under the destination", jellyfin.DefaultLayout.Movies, clingy.Advanced).(string)
	c.layout.Shows = params.Flag("shows-dir", "show library directory under the destination", jellyfin.DefaultLayout.Shows, clingy.Advanced).(string)
	c.layout.Music = params.Flag("music-dir", "music library directory under the destination", jellyfin.DefaultLayout.Music, clingy.Advanced).(string)
	c.layout.Unidentified = params.Flag("unidentified-dir", "directory under the destination for unidentified files", jellyfin.DefaultLayout.Unidentified, clingy.Advanced).(string)

	c.src = params.Arg("source", "directory with the files to organize").(string)
	c.dst = params.Arg("destination", "root directory of the Jellyfin libraries").(string)
}

// app holds what a command needs after the configuration is valid.
type app struct {
	store      *store.Store
	scanner    *engine.Scanner
	opts       engine.Options
	mode       fsop.Mode
	providers  string
	identifier *ident.Identifier
}

func (c *config) open(log *slog.Logger) (*app, error) {
	src, err := filepath.Abs(c.src)
	if err != nil {
		return nil, err
	}
	dst, err := filepath.Abs(c.dst)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", src)
	}
	if dfi, statErr := os.Stat(dst); statErr == nil && !dfi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dst)
	}
	if err = c.layout.Validate(); err != nil {
		return nil, err
	}
	if c.jobs < 1 {
		return nil, errors.New("--jobs must be 1 or more")
	}

	s, err := store.Open(c.db)
	if err != nil {
		return nil, err
	}

	x := &ident.Identifier{
		Store:       s,
		SRRDB:       srrdb.New(provider.New(s, srrdb.RequestsPerSecond, log)),
		MusicBrainz: musicbrainz.New(provider.New(s, musicbrainz.RequestsPerSecond, log)),
		Log:         log,
	}
	status := map[string]string{"srrDB": "✓", "MusicBrainz": "✓"}
	if c.osKey != "" {
		x.OpenSubtitles = opensubtitles.New(provider.New(s, opensubtitles.RequestsPerSecond, log), c.osKey)
	}
	if c.tmdbKey != "" {
		x.TMDB = tmdb.New(provider.New(s, tmdb.RequestsPerSecond, log), c.tmdbKey)
	}
	if c.acoustidKey != "" {
		x.AcoustID = acoustid.New(provider.New(s, acoustid.RequestsPerSecond, log), c.acoustidKey)
	}
	if path, err := exec.LookPath("fpcalc"); err == nil {
		x.FPCalc = path
	}

	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	}
	status["OpenSubtitles"] = mark(x.OpenSubtitles != nil)
	status["TMDB"] = mark(x.TMDB != nil)
	status["AcoustID"] = mark(x.AcoustID != nil)
	status["fpcalc"] = mark(x.FPCalc != "")
	var parts []string
	for _, name := range []string{"OpenSubtitles", "srrDB", "TMDB", "AcoustID", "fpcalc", "MusicBrainz"} {
		parts = append(parts, name+" "+status[name])
	}

	mode := fsop.Copy
	if c.move {
		mode = fsop.Move
	}

	return &app{
		store:      s,
		scanner:    &engine.Scanner{Identifier: x, Log: log},
		opts:       engine.Options{Source: src, Dest: dst, Recursive: c.recursive, Jobs: c.jobs},
		mode:       mode,
		providers:  strings.Join(parts, "  "),
		identifier: x,
	}, nil
}
