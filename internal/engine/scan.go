// Package engine scans a source directory, identifies its media files, and
// plans where each file goes in the Jellyfin library.
package engine

import (
	"cmp"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/amwolff/jellybot/internal/ident"
	"github.com/amwolff/jellybot/internal/media"
)

// Options configure a scan.
type Options struct {
	// Source and Dest are absolute, clean paths.
	Source    string
	Dest      string
	Recursive bool
	Jobs      int
}

// Event is a scan event. Gen is the generation of the scan; a plan ignores
// events from earlier generations.
type Event interface {
	generation() int
}

// Found reports a media file.
type Found struct {
	Gen int
	Row Row
}

// Progress reports the stage of an identification.
type Progress struct {
	Gen   int
	Rel   string
	Stage string
}

// Identified reports the outcome of an identification.
type Identified struct {
	Gen    int
	Rel    string
	Result ident.Result
	Err    error
}

// Done reports the end of a scan. Err is set if the walk failed.
type Done struct {
	Gen int
	Err error
}

func (e Found) generation() int      { return e.Gen }
func (e Progress) generation() int   { return e.Gen }
func (e Identified) generation() int { return e.Gen }
func (e Done) generation() int       { return e.Gen }

// Scanner finds and identifies media files.
type Scanner struct {
	Identifier *ident.Identifier
	Log        *slog.Logger
}

// Scan walks opts.Source and emits a Found event for each media file. Then
// it identifies the files with opts.Jobs workers. Done is the last event.
// Scan returns when the scan is complete or ctx is canceled.
func (s *Scanner) Scan(ctx context.Context, gen int, opts Options, emit func(Event)) {
	rows, err := Walk(opts)
	if err != nil {
		emit(Done{Gen: gen, Err: err})
		return
	}
	for _, r := range rows {
		emit(Found{Gen: gen, Row: r})
	}

	work := make(chan Row)
	var wg sync.WaitGroup
	for range max(opts.Jobs, 1) {
		wg.Go(func() {
			for r := range work {
				s.identify(ctx, gen, opts, r, emit)
			}
		})
	}
	for _, r := range rows {
		select {
		case work <- r:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()

	emit(Done{Gen: gen, Err: ctx.Err()})
}

// Identify identifies one row again, for example after a refresh request.
func (s *Scanner) Identify(ctx context.Context, gen int, opts Options, r Row, emit func(Event)) {
	s.identify(ctx, gen, opts, r, emit)
}

func (s *Scanner) identify(ctx context.Context, gen int, opts Options, r Row, emit func(Event)) {
	if ctx.Err() != nil {
		return
	}
	f := ident.File{
		Path:    filepath.Join(opts.Source, r.Rel),
		Size:    r.Size,
		ModTime: r.ModTime,
		Kind:    r.Kind,
	}
	res, err := s.Identifier.Identify(ctx, f, func(stage string) {
		emit(Progress{Gen: gen, Rel: r.Rel, Stage: stage})
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil && s.Log != nil {
		s.Log.WarnContext(ctx, "identification failed", "path", f.Path, "error", err)
	}
	emit(Identified{Gen: gen, Rel: r.Rel, Result: res, Err: err})
}

// Walk lists the media files of opts.Source, with their sidecars, sorted by
// path. It ignores hidden files and directories, and the destination if it
// is inside the source.
func Walk(opts Options) ([]Row, error) {
	byDir := make(map[string][]fs.DirEntry)

	if opts.Recursive {
		err := filepath.WalkDir(opts.Source, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subdirectory must not stop the scan.
				if path != opts.Source && d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if path != opts.Source && (hidden(d.Name()) || path == opts.Dest) {
					return fs.SkipDir
				}
				return nil
			}
			dir := filepath.Dir(path)
			byDir[dir] = append(byDir[dir], d)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		entries, err := os.ReadDir(opts.Source)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				byDir[opts.Source] = append(byDir[opts.Source], e)
			}
		}
	}

	var rows []Row
	for dir, entries := range byDir {
		var mains, sidecars []fs.DirEntry
		for _, e := range entries {
			if hidden(e.Name()) || !e.Type().IsRegular() {
				continue
			}
			switch media.Classify(e.Name()) {
			case media.Video, media.Audio:
				mains = append(mains, e)
			case media.Sidecar:
				sidecars = append(sidecars, e)
			}
		}

		rel := func(name string) string {
			r, err := filepath.Rel(opts.Source, filepath.Join(dir, name))
			if err != nil {
				return name
			}
			return r
		}

		byName := make(map[string]*Row, len(mains))
		order := make([]string, 0, len(mains))
		for _, e := range mains {
			info, err := e.Info()
			if err != nil {
				continue
			}
			byName[e.Name()] = &Row{
				Rel:     rel(e.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
				Kind:    media.Classify(e.Name()),
				Choice:  -1,
			}
			order = append(order, e.Name())
		}

		// A sidecar goes with the main file whose base name it matches
		// for the most characters.
		for _, sc := range sidecars {
			var best string
			var bestSuffix string
			for _, name := range order {
				suffix, ok := media.SidecarSuffix(name, sc.Name())
				if ok && (best == "" || len(suffix) < len(bestSuffix)) {
					best, bestSuffix = name, suffix
				}
			}
			if best == "" {
				continue
			}
			info, err := sc.Info()
			if err != nil {
				continue
			}
			r := byName[best]
			r.Sidecars = append(r.Sidecars, Sidecar{Rel: rel(sc.Name()), Suffix: bestSuffix, Size: info.Size()})
		}

		for _, name := range order {
			r := byName[name]
			slices.SortFunc(r.Sidecars, func(a, b Sidecar) int { return cmp.Compare(a.Rel, b.Rel) })
			rows = append(rows, *r)
		}
	}

	slices.SortFunc(rows, func(a, b Row) int { return cmp.Compare(a.Rel, b.Rel) })
	return rows, nil
}

func hidden(name string) bool {
	return strings.HasPrefix(name, ".")
}
