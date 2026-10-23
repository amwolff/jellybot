// Package fsop copies and moves files into the library. The destination is
// always byte-identical to the source, and an existing file is never
// overwritten.
package fsop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrExists is returned when a file is already at the destination.
var ErrExists = errors.New("fsop: destination exists")

// ErrMismatch is returned when the copy differs from the source.
var ErrMismatch = errors.New("fsop: the copy differs from the source")

// Mode is the apply mode.
type Mode int

// Apply modes.
const (
	Copy Mode = iota
	Move
)

func (m Mode) String() string {
	if m == Move {
		return "move"
	}
	return "copy"
}

// Applier copies or moves files.
type Applier struct {
	Mode  Mode
	Modes Modes
	Owner Owner
	// Progress, if set, receives the progress of the current file. For a
	// copy, total is twice the size: the copy and the second read.
	Progress func(done, total int64)

	// Test hooks.
	rename       func(oldpath, newpath string) error
	beforeVerify func(tmpPath string)
}

func (a *Applier) doRename(oldpath, newpath string) error {
	if a.rename != nil {
		return a.rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (a *Applier) progress(done, total int64) {
	if a.Progress != nil {
		a.Progress(done, total)
	}
}

// Apply copies or moves the regular file at src to dst. It creates the
// directories of dst that do not exist.
func (a *Applier) Apply(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("fsop: %s is not a regular file", src)
	}

	if err = a.mkdirs(filepath.Dir(dst)); err != nil {
		return err
	}
	if err = mustNotExist(dst); err != nil {
		return err
	}

	if a.Mode == Copy {
		return a.copy(ctx, src, dst, fi)
	}

	err = a.doRename(src, dst)
	switch {
	case err == nil:
		a.progress(fi.Size(), fi.Size())
		if oerr := a.override(dst); oerr != nil {
			// Put the file back, so that the row can be tried again.
			if rerr := a.doRename(dst, src); rerr != nil {
				return errors.Join(oerr, rerr)
			}
			return oerr
		}
		return nil
	case !errors.Is(err, syscall.EXDEV):
		return err
	}

	// A different filesystem: copy, then remove the source.
	if err := a.copy(ctx, src, dst, fi); err != nil {
		return err
	}
	return os.Remove(src)
}

func mustNotExist(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%w: %s", ErrExists, path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return err
}

// mkdirs creates each directory of dir that does not exist, and applies
// the directory overrides to the directories that it creates.
func (a *Applier) mkdirs(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		_, err := os.Stat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}

	for i := len(missing) - 1; i >= 0; i-- {
		d := missing[i]
		// Jellyfin usually runs as another user, who must read the library.
		if err := os.Mkdir(d, 0o755); err != nil { //nolint:gosec // see above
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		}
		if a.Owner.Set {
			if err := os.Chown(d, a.Owner.UID, a.Owner.GID); err != nil {
				return err
			}
		}
		if a.Modes.HasDir {
			if err := os.Chmod(d, a.Modes.Dir); err != nil {
				return err
			}
		}
	}
	return nil
}

// override applies the file overrides to a moved file.
func (a *Applier) override(path string) error {
	if a.Owner.Set {
		if err := os.Chown(path, a.Owner.UID, a.Owner.GID); err != nil {
			return err
		}
	}
	if a.Modes.HasFile {
		return os.Chmod(path, a.Modes.File)
	}
	return nil
}

func (a *Applier) copy(ctx context.Context, src, dst string, fi fs.FileInfo) (err error) {
	in, err := os.Open(src) //nolint:gosec // copying the files that the user selected is the purpose
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".jellybot-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	size := fi.Size()
	total := 2 * size
	a.progress(0, total)

	srcSum := sha256.New()
	r := &reader{ctx: ctx, r: in, onRead: func(n int64) { a.progress(n, total) }}
	n, err := io.CopyBuffer(io.MultiWriter(tmp, srcSum), r, make([]byte, 1<<20))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("fsop: copied %d bytes of %d from %s", n, size, src)
	}
	if err = tmp.Close(); err != nil {
		return err
	}

	if a.beforeVerify != nil {
		a.beforeVerify(tmpPath)
	}
	dstSum, err := sum(ctx, tmpPath, func(n int64) { a.progress(size+n, total) })
	if err != nil {
		return err
	}
	if !bytes.Equal(srcSum.Sum(nil), dstSum) {
		return fmt.Errorf("%w: %s", ErrMismatch, src)
	}

	if a.Owner.Set {
		if err := os.Chown(tmpPath, a.Owner.UID, a.Owner.GID); err != nil {
			return err
		}
	}
	mode := fi.Mode().Perm()
	if a.Modes.HasFile {
		mode = a.Modes.File
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if err := os.Chtimes(tmpPath, time.Time{}, fi.ModTime()); err != nil {
		return err
	}

	if err := mustNotExist(dst); err != nil {
		return err
	}
	return a.doRename(tmpPath, dst)
}

// sum returns the SHA-256 of the file at path.
func sum(ctx context.Context, path string, onRead func(int64)) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the temporary copy that jellybot wrote
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.CopyBuffer(h, &reader{ctx: ctx, r: f, onRead: onRead}, make([]byte, 1<<20)); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// reader stops at context cancellation and reports the bytes it read.
type reader struct {
	ctx    context.Context
	r      io.Reader
	n      int64
	onRead func(int64)
}

func (r *reader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	r.n += int64(n)
	if r.onRead != nil && n > 0 {
		r.onRead(r.n)
	}
	return n, err
}
