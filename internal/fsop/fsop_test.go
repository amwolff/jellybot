package fsop

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func writeSource(t *testing.T, dir string, size int) (string, []byte, time.Time) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	path := filepath.Join(dir, "the other movie with Mace Windu.mkv")
	// 0640 differs from the 0600 of temporary files, so the tests see
	// whether the copy keeps the mode.
	if err := os.WriteFile(path, data, 0o640); err != nil { //nolint:gosec // see above
		t.Fatal(err)
	}
	mtime := time.Date(2014, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(path, time.Time{}, mtime); err != nil {
		t.Fatal(err)
	}
	return path, data, mtime
}

func assertFile(t *testing.T, path string, data []byte, mtime time.Time, perm fs.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path) //nolint:gosec // test paths
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s differs from the source", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mtime) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), mtime)
	}
	if fi.Mode().Perm() != perm {
		t.Fatalf("mode = %v, want %v", fi.Mode().Perm(), perm)
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".jellybot-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func dstPath(root string) string {
	return filepath.Join(root, "Movies", "The Matrix (1999) [tmdbid-603]", "The Matrix (1999) [tmdbid-603].mkv")
}

func TestCopy(t *testing.T) {
	src, data, mtime := writeSource(t, t.TempDir(), 3<<20+17)
	dst := dstPath(t.TempDir())

	var last, total int64
	a := &Applier{Mode: Copy, Progress: func(d, tt int64) { last, total = d, tt }}
	if err := a.Apply(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dst, data, mtime, 0o640)
	assertFile(t, src, data, mtime, 0o640)
	assertNoTemp(t, filepath.Dir(dst))
	if total != 2*int64(len(data)) || last != total {
		t.Fatalf("progress = %d/%d", last, total)
	}
}

func TestMove(t *testing.T) {
	dir := t.TempDir()
	src, data, mtime := writeSource(t, dir, 1000)
	dst := dstPath(dir)

	a := &Applier{Mode: Move}
	if err := a.Apply(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dst, data, mtime, 0o640)
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
}

func TestMoveAcrossFilesystems(t *testing.T) {
	src, data, mtime := writeSource(t, t.TempDir(), 300_000)
	dst := dstPath(t.TempDir())

	var renames int
	a := &Applier{Mode: Move}
	a.rename = func(oldpath, newpath string) error {
		renames++
		if oldpath == src {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		return os.Rename(oldpath, newpath)
	}
	if err := a.Apply(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dst, data, mtime, 0o640)
	assertNoTemp(t, filepath.Dir(dst))
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	if renames != 2 {
		t.Fatalf("renames = %d, want 2", renames)
	}
}

func TestNeverOverwrite(t *testing.T) {
	for _, mode := range []Mode{Copy, Move} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			src, data, mtime := writeSource(t, dir, 1000)
			dst := dstPath(dir)
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, []byte("precious"), 0o600); err != nil {
				t.Fatal(err)
			}

			a := &Applier{Mode: mode}
			if err := a.Apply(context.Background(), src, dst); !errors.Is(err, ErrExists) {
				t.Fatalf("err = %v, want ErrExists", err)
			}
			if got, _ := os.ReadFile(dst); string(got) != "precious" { //nolint:gosec // test paths
				t.Fatalf("destination changed to %q", got)
			}
			assertFile(t, src, data, mtime, 0o640)
		})
	}
}

func TestMismatchLeavesNothing(t *testing.T) {
	for _, mode := range []Mode{Copy, Move} {
		t.Run(mode.String(), func(t *testing.T) {
			src, data, mtime := writeSource(t, t.TempDir(), 200_000)
			dst := dstPath(t.TempDir())

			a := &Applier{Mode: mode}
			a.rename = func(oldpath, newpath string) error {
				if oldpath == src {
					return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
				}
				return os.Rename(oldpath, newpath)
			}
			a.beforeVerify = func(tmpPath string) {
				f, err := os.OpenFile(tmpPath, os.O_RDWR, 0) //nolint:gosec // test paths
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteAt([]byte{0xFF ^ data[1234]}, 1234); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			}

			if err := a.Apply(context.Background(), src, dst); !errors.Is(err, ErrMismatch) {
				t.Fatalf("err = %v, want ErrMismatch", err)
			}
			if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("destination exists: %v", err)
			}
			assertNoTemp(t, filepath.Dir(dst))
			assertFile(t, src, data, mtime, 0o640)
		})
	}
}

func TestFinalRenameFailureLeavesNothing(t *testing.T) {
	src, _, _ := writeSource(t, t.TempDir(), 1000)
	dst := dstPath(t.TempDir())
	boom := errors.New("boom")

	a := &Applier{Mode: Copy}
	a.rename = func(string, string) error { return boom }
	if err := a.Apply(context.Background(), src, dst); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	assertNoTemp(t, filepath.Dir(dst))
}

func TestCanceled(t *testing.T) {
	src, _, _ := writeSource(t, t.TempDir(), 3<<20)
	dst := dstPath(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	a := &Applier{Mode: Copy, Progress: func(done, _ int64) {
		if done > 0 {
			cancel()
		}
	}}
	if err := a.Apply(ctx, src, dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoTemp(t, filepath.Dir(dst))
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination exists: %v", err)
	}
}

func TestOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes and owners")
	}
	modes, err := ParseModes("D0750,F0604")
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{UID: os.Getuid(), GID: os.Getgid(), Set: true}

	for _, mode := range []Mode{Copy, Move} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			src, data, mtime := writeSource(t, root, 1000)

			lib := filepath.Join(root, "lib")
			if err := os.Mkdir(lib, 0o700); err != nil {
				t.Fatal(err)
			}
			dst := dstPath(lib)

			a := &Applier{Mode: mode, Modes: modes, Owner: owner}
			if err := a.Apply(context.Background(), src, dst); err != nil {
				t.Fatal(err)
			}
			assertFile(t, dst, data, mtime, 0o604)

			for _, d := range []string{filepath.Join(lib, "Movies"), filepath.Dir(dst)} {
				fi, err := os.Stat(d)
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode().Perm() != 0o750 {
					t.Fatalf("%s mode = %v, want 0750", d, fi.Mode().Perm())
				}
			}
			// A directory that existed before keeps its mode.
			if fi, _ := os.Stat(lib); fi.Mode().Perm() != 0o700 {
				t.Fatalf("existing directory mode changed to %v", fi.Mode().Perm())
			}
		})
	}
}

func TestParseModes(t *testing.T) {
	tests := []struct {
		in   string
		want Modes
		str  string
	}{
		{"D0700,F0600", Modes{Dir: 0o700, File: 0o600, HasDir: true, HasFile: true}, "D0700,F0600"},
		{"F644", Modes{File: 0o644, HasFile: true}, "F0644"},
		{"0755", Modes{Dir: 0o755, File: 0o755, HasDir: true, HasFile: true}, "D0755,F0755"},
		{"D2775, F0664", Modes{Dir: 0o775 | fs.ModeSetgid, File: 0o664, HasDir: true, HasFile: true}, "D2775,F0664"},
		{"0700,F0600", Modes{Dir: 0o700, File: 0o600, HasDir: true, HasFile: true}, "D0700,F0600"},
		{"", Modes{}, ""},
	}
	for _, tt := range tests {
		got, err := ParseModes(tt.in)
		if err != nil {
			t.Errorf("ParseModes(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseModes(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
		if s := got.String(); s != tt.str {
			t.Errorf("String() = %q, want %q", s, tt.str)
		}
	}

	for _, bad := range []string{"D", "F0800", "u+rwx", "D0700,,F0600", "17777", "Dx", "D-1", "0o755"} {
		if _, err := ParseModes(bad); err == nil {
			t.Errorf("ParseModes(%q) succeeded", bad)
		}
	}
}

func TestParseOwner(t *testing.T) {
	got, err := ParseOwner("1000:1000")
	if err != nil || got != (Owner{UID: 1000, GID: 1000, Set: true}) || got.String() != "1000:1000" {
		t.Fatalf("ParseOwner = %+v, %v", got, err)
	}
	if got, err := ParseOwner(""); err != nil || got.Set {
		t.Fatalf("ParseOwner(\"\") = %+v, %v", got, err)
	}
	for _, bad := range []string{"1000", "user:group", "1000:", ":1000", "-1:0", "1:2:3"} {
		if _, err := ParseOwner(bad); err == nil {
			t.Errorf("ParseOwner(%q) succeeded", bad)
		}
	}
}

func TestMoveRollsBackWhenOverrideFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a user that cannot chown to another user")
	}
	dir := t.TempDir()
	src, data, mtime := writeSource(t, dir, 1000)
	dst := dstPath(dir)

	a := &Applier{Mode: Move, Owner: Owner{UID: os.Getuid() + 1, GID: os.Getgid(), Set: true}}
	err := a.Apply(context.Background(), src, dst)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v, want a permission error", err)
	}
	assertFile(t, src, data, mtime, 0o640)
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination exists: %v", err)
	}
}
