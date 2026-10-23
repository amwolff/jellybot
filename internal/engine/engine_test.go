package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/amwolff/jellybot/internal/fsop"
	"github.com/amwolff/jellybot/internal/ident"
	"github.com/amwolff/jellybot/internal/jellyfin"
	"github.com/amwolff/jellybot/internal/provider/providertest"
	"github.com/amwolff/jellybot/internal/store"
)

func write(t *testing.T, root, rel string, size int) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte(rel), size/max(len(rel), 1)+1)[:size]
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rels(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Rel)
		for _, sc := range r.Sidecars {
			out = append(out, "  "+sc.Rel+" "+sc.Suffix)
		}
	}
	return out
}

func TestWalk(t *testing.T) {
	src := t.TempDir()
	write(t, src, "the other movie with Mace Windu.mkv", 10)
	write(t, src, "the other movie with Mace Windu.en.srt", 10)
	write(t, src, "the other movie with Mace Windu.en.forced.srt", 10)
	write(t, src, "song.flac", 10)
	write(t, src, "song.lrc", 10)
	write(t, src, "a.mkv", 10)
	write(t, src, "a.b.mkv", 10)
	write(t, src, "a.b.srt", 10) // belongs to a.b.mkv, the longer match
	write(t, src, "orphan.srt", 10)
	write(t, src, "notes.txt", 10)
	write(t, src, ".hidden.mkv", 10)
	write(t, src, "sub/ep1 lol.mkv", 10)
	write(t, src, ".git/x.mkv", 10)
	write(t, src, "library/Movies/x.mkv", 10)

	got, err := Walk(Options{Source: src, Dest: filepath.Join(src, "library")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a.b.mkv",
		"  a.b.srt .srt",
		"a.mkv",
		"song.flac",
		"  song.lrc .lrc",
		"the other movie with Mace Windu.mkv",
		"  the other movie with Mace Windu.en.forced.srt .en.forced.srt",
		"  the other movie with Mace Windu.en.srt .en.srt",
	}
	if g := rels(got); !slices.Equal(g, want) {
		t.Fatalf("Walk =\n%s\nwant\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}

	got, err = Walk(Options{Source: src, Dest: filepath.Join(src, "library"), Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	want = append(want[:5:5], filepath.Join("sub", "ep1 lol.mkv"))
	want = append(want,
		"the other movie with Mace Windu.mkv",
		"  the other movie with Mace Windu.en.forced.srt .en.forced.srt",
		"  the other movie with Mace Windu.en.srt .en.srt",
	)
	if g := rels(got); !slices.Equal(g, want) {
		t.Fatalf("recursive Walk =\n%s\nwant\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
}

func movie(title string, year, tmdbID int) *ident.Identity {
	return &ident.Identity{Kind: ident.KindMovie, Title: title, Year: year, TMDBID: tmdbID}
}

func newPlan(t *testing.T, src, dst string, rows ...Row) *Plan {
	t.Helper()
	p := NewPlan(Options{Source: src, Dest: dst}, jellyfin.DefaultLayout, 1)
	for _, r := range rows {
		r.Choice = -1
		p.Handle(Found{Gen: 1, Row: r})
	}
	return p
}

func TestPlanDestinations(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := newPlan(t, src, dst,
		Row{Rel: "matrix.mkv", Size: 10, Sidecars: []Sidecar{{Rel: "matrix.en.srt", Suffix: ".en.srt", Size: 3}}},
		Row{Rel: "home.mp4", Size: 20},
		Row{Rel: "which.mkv", Size: 30},
		Row{Rel: "dup1.mkv", Size: 40},
		Row{Rel: "dup2.mkv", Size: 50},
	)

	if p.Handle(Identified{Gen: 2, Rel: "matrix.mkv"}) {
		t.Fatal("handled an event of another generation")
	}

	p.Handle(Progress{Gen: 1, Rel: "matrix.mkv", Stage: ident.StageHashing})
	if r := p.Row("matrix.mkv"); r.State != StateWorking || r.Text() != "hashing…" {
		t.Fatalf("row = %+v, text %q", r, r.Text())
	}

	p.Handle(Identified{Gen: 1, Rel: "matrix.mkv", Result: ident.Result{Identity: movie("The Matrix", 1999, 603)}})
	p.Handle(Identified{Gen: 1, Rel: "home.mp4", Result: ident.Result{Reason: "OpenSubtitles: no match"}})
	p.Handle(Identified{Gen: 1, Rel: "which.mkv", Result: ident.Result{Candidates: []ident.Identity{
		*movie("The Matrix", 1999, 603), *movie("The Matrix Reloaded", 2003, 604),
	}}})
	p.Handle(Identified{Gen: 1, Rel: "dup1.mkv", Result: ident.Result{Identity: movie("Alien", 1979, 348)}})
	p.Handle(Identified{Gen: 1, Rel: "dup2.mkv", Result: ident.Result{Identity: movie("Alien", 1979, 348)}})

	matrix := p.Row("matrix.mkv")
	wantDest := filepath.FromSlash("Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv")
	if matrix.Status != StatusReady || matrix.Dest != wantDest {
		t.Fatalf("matrix = %v %q", matrix.Status, matrix.Dest)
	}
	if home := p.Row("home.mp4"); home.Status != StatusNone || home.Text() != "✗ unmatched: OpenSubtitles: no match" {
		t.Fatalf("home = %v %q", home.Status, home.Text())
	}
	if which := p.Row("which.mkv"); which.Status != StatusNone || which.Text() != "⚠ ambiguous (2 candidates)" {
		t.Fatalf("which = %v %q", which.Status, which.Text())
	}
	for _, rel := range []string{"dup1.mkv", "dup2.mkv"} {
		if r := p.Row(rel); r.Status != StatusConflict || !strings.HasPrefix(r.Note, "same destination as ") {
			t.Fatalf("%s = %v %q", rel, r.Status, r.Note)
		}
	}

	// u sends an unmatched row to Unidentified.
	home := p.Row("home.mp4")
	home.Unidentified = true
	p.Recompute()
	if home.Status != StatusReady || home.Dest != filepath.Join("Unidentified", "home.mp4") || home.Text() != "→ "+home.Dest {
		t.Fatalf("home = %v %q %q", home.Status, home.Dest, home.Text())
	}

	// c selects the second candidate, which then has the same destination as
	// nothing else.
	which := p.Row("which.mkv")
	which.Choice = 1
	p.Recompute()
	if which.Status != StatusReady || !strings.Contains(which.Dest, "The Matrix Reloaded (2003)") {
		t.Fatalf("which = %v %q", which.Status, which.Dest)
	}
	// The first candidate collides with matrix.mkv.
	which.Choice = 0
	p.Recompute()
	if which.Status != StatusConflict || matrix.Status != StatusConflict {
		t.Fatalf("which = %v, matrix = %v", which.Status, matrix.Status)
	}
	// Skipping one of them solves the conflict.
	which.Skip = true
	p.Recompute()
	if matrix.Status != StatusReady {
		t.Fatalf("matrix = %v %q", matrix.Status, matrix.Note)
	}

	// Skipping dup2 solves the other conflict.
	p.Row("dup2.mkv").Skip = true
	p.Recompute()

	ops := p.Ops()
	var got []string
	for _, op := range ops {
		r1, _ := filepath.Rel(src, op.Src)
		r2, _ := filepath.Rel(dst, op.Dst)
		got = append(got, r1+" → "+r2)
	}
	// Ops follow the order of the rows, which is the order of the Found
	// events (Walk sorts them).
	want := []string{
		"matrix.mkv → " + wantDest,
		"matrix.en.srt → " + filepath.FromSlash("Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].en.srt"),
		"home.mp4 → " + filepath.Join("Unidentified", "home.mp4"),
		"dup1.mkv → " + filepath.FromSlash("Movies/Alien (1979) [tmdbid-348]/Alien (1979) [tmdbid-348].mkv"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Ops =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	c := p.Counts()
	if c.Total != 5 || c.Matched != 3 || c.Ambiguous != 1 || c.Unmatched != 1 || c.Ready != 3 || c.ReadyBytes != 10+3+20+40 || c.Conflicts != 0 {
		t.Fatalf("Counts = %+v", c)
	}
}

func TestPlanDisk(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "same.mkv", 200_000)
	write(t, src, "different.mkv", 200_000)
	write(t, src, "sidecar.mkv", 10)
	write(t, src, "sidecar.en.srt", 10)

	p := newPlan(t, src, dst,
		Row{Rel: "same.mkv", Size: 200_000},
		Row{Rel: "different.mkv", Size: 200_000},
		Row{Rel: "sidecar.mkv", Size: 10, Sidecars: []Sidecar{{Rel: "sidecar.en.srt", Suffix: ".en.srt", Size: 10}}},
	)
	ids := map[string]*ident.Identity{
		"same.mkv":      movie("Same", 2000, 1),
		"different.mkv": movie("Different", 2000, 2),
		"sidecar.mkv":   movie("Sidecar", 2000, 3),
	}
	for rel, id := range ids {
		dest, err := jellyfin.DefaultLayout.Path(*id, ".mkv")
		if err != nil {
			t.Fatal(err)
		}
		switch rel {
		case "same.mkv":
			data, _ := os.ReadFile(filepath.Join(src, rel)) //nolint:gosec // test paths
			write(t, dst, dest, 0)
			if err := os.WriteFile(filepath.Join(dst, dest), data, 0o600); err != nil { //nolint:gosec // test paths
				t.Fatal(err)
			}
		case "different.mkv":
			write(t, dst, dest, 200_000)
		case "sidecar.mkv":
			write(t, dst, jellyfin.SidecarPath(dest, ".en.srt"), 10)
		}
	}
	for rel, id := range ids {
		p.Handle(Identified{Gen: 1, Rel: rel, Result: ident.Result{Identity: id}})
	}

	if r := p.Row("same.mkv"); r.Status != StatusPresent {
		t.Fatalf("same = %v %q", r.Status, r.Note)
	}
	if r := p.Row("different.mkv"); r.Status != StatusConflict || !strings.HasPrefix(r.Note, "a different file is at") {
		t.Fatalf("different = %v %q", r.Status, r.Note)
	}
	if r := p.Row("sidecar.mkv"); r.Status != StatusConflict || !strings.HasSuffix(r.Note, ".en.srt") {
		t.Fatalf("sidecar = %v %q", r.Status, r.Note)
	}
	if len(p.Ops()) != 0 {
		t.Fatalf("Ops = %+v", p.Ops())
	}

	// After the file at the destination goes away and the disk check is
	// invalidated, the row is ready.
	if err := os.RemoveAll(filepath.Join(dst, "Movies", "Different (2000) [tmdbid-2]")); err != nil {
		t.Fatal(err)
	}
	p.Recompute()
	if r := p.Row("different.mkv"); r.Status != StatusConflict {
		t.Fatal("the disk check was not cached")
	}
	p.InvalidateDisk()
	p.Recompute()
	if r := p.Row("different.mkv"); r.Status != StatusReady {
		t.Fatalf("different = %v %q", r.Status, r.Note)
	}
}

func TestPlanInPlace(t *testing.T) {
	root := t.TempDir()
	rel := filepath.FromSlash("Movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv")
	write(t, root, rel, 10)

	p := newPlan(t, root, root, Row{Rel: rel, Size: 10})
	p.Handle(Identified{Gen: 1, Rel: rel, Result: ident.Result{Identity: movie("The Matrix", 1999, 603)}})
	if r := p.Row(rel); r.Status != StatusInPlace || len(p.Ops()) != 0 {
		t.Fatalf("row = %v %q", r.Status, r.Note)
	}
}

func TestScan(t *testing.T) {
	src := t.TempDir()
	write(t, src, "a.mkv", 200_000)
	write(t, src, "b.mkv", 200_000)
	write(t, src, "sub/c.mkv", 200_000)

	s := providertest.Store(t)
	sc := &Scanner{Identifier: &ident.Identifier{Store: s}}
	opts := Options{Source: src, Dest: t.TempDir(), Jobs: 2}

	run := func(opts Options) *Plan {
		p := NewPlan(opts, jellyfin.DefaultLayout, 7)
		var mu sync.Mutex
		sc.Scan(context.Background(), 7, opts, func(e Event) {
			mu.Lock()
			defer mu.Unlock()
			p.Handle(e)
		})
		return p
	}

	p := run(opts)
	if !p.Done || p.Err != nil || len(p.Rows) != 2 {
		t.Fatalf("plan: done %v, err %v, rows %d", p.Done, p.Err, len(p.Rows))
	}
	for _, r := range p.Rows {
		// Without a TMDB key, videos are unmatched.
		if r.State != StateUnmatched || !strings.HasPrefix(r.Result.Reason, "no TMDB key") {
			t.Fatalf("row %s = %v %q", r.Rel, r.State, r.Result.Reason)
		}
	}

	opts.Recursive = true
	if p := run(opts); len(p.Rows) != 3 {
		t.Fatalf("recursive rows = %d", len(p.Rows))
	}

	// A canceled scan stops and still reports Done.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var done bool
	sc.Scan(ctx, 1, opts, func(e Event) {
		if d, ok := e.(Done); ok {
			done = d.Err != nil
		}
	})
	if !done {
		t.Fatal("canceled scan did not report Done with an error")
	}
}

func TestHumanBytes(t *testing.T) {
	tests := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 34_500_000_000: "32.1 GiB"}
	for n, want := range tests {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRun(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "matrix.mkv", 200_000)
	write(t, src, "matrix.en.srt", 100)
	write(t, src, "alien.mkv", 300)
	write(t, src, "alien.en.srt", 100)

	s := providertest.Store(t)
	p := newPlan(t, src, dst,
		Row{Rel: "alien.mkv", Size: 300, Sidecars: []Sidecar{{Rel: "alien.en.srt", Suffix: ".en.srt", Size: 100}}},
		Row{Rel: "matrix.mkv", Size: 200_000, Sidecars: []Sidecar{{Rel: "matrix.en.srt", Suffix: ".en.srt", Size: 100}}},
	)
	p.Handle(Identified{Gen: 1, Rel: "matrix.mkv", Result: ident.Result{Identity: movie("The Matrix", 1999, 603)}})
	p.Handle(Identified{Gen: 1, Rel: "alien.mkv", Result: ident.Result{Identity: movie("Alien", 1979, 348)}})

	// A memo for the source must follow the file.
	if err := s.PutStat(filepath.Join(src, "matrix.mkv"), store.StatEntry{Size: 200_000, OSHash: 42}); err != nil {
		t.Fatal(err)
	}

	// Remove alien.mkv after planning, so that its operation fails and its
	// sidecar is skipped.
	if err := os.Remove(filepath.Join(src, "alien.mkv")); err != nil {
		t.Fatal(err)
	}

	var progress []ApplyProgress
	var results []OpDone
	Run(context.Background(), &fsop.Applier{Mode: fsop.Copy}, s, p.Ops(),
		func(ap ApplyProgress) { progress = append(progress, ap) },
		func(d OpDone) { results = append(results, d); p.Applied(d) })

	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Rel != "alien.mkv" || results[0].Err == nil {
		t.Fatalf("alien = %+v", results[0])
	}
	if results[1].Err != nil || results[2].Err != nil || !results[2].Sidecar {
		t.Fatalf("matrix = %+v %+v", results[1], results[2])
	}

	matrix := p.Row("matrix.mkv")
	if !matrix.Applied || matrix.ApplyErr != nil || !strings.HasPrefix(matrix.Text(), "✓ ") {
		t.Fatalf("matrix = %+v", matrix)
	}
	if alien := p.Row("alien.mkv"); alien.Applied || alien.ApplyErr == nil {
		t.Fatalf("alien = %+v", alien)
	}
	// The failed row stays ready, so that the user can try again.
	if c := p.Counts(); c.Ready != 1 {
		t.Fatalf("Counts.Ready = %d after apply, want 1", c.Ready)
	}

	for _, rel := range []string{"matrix.mkv", "matrix.en.srt"} {
		want, _ := os.ReadFile(filepath.Join(src, rel)) //nolint:gosec // test paths
		d := filepath.Join(dst, jellyfin.SidecarPath(matrix.Dest, strings.TrimPrefix(rel, "matrix")))
		got, err := os.ReadFile(d) //nolint:gosec // test paths
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v", d, err)
		}
	}
	if e, ok, _ := s.Stat(filepath.Join(dst, matrix.Dest)); !ok || e.OSHash != 42 {
		t.Fatalf("memo for the destination = %+v, %v", e, ok)
	}

	last := progress[len(progress)-1]
	if last.Total != 200_000+100+300+100 || last.Done > last.Total {
		t.Fatalf("last progress = %+v", last)
	}
}
