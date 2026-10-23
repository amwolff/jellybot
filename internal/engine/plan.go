package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/amwolff/jellybot/internal/ident"
	"github.com/amwolff/jellybot/internal/jellyfin"
	"github.com/amwolff/jellybot/internal/media"
	"github.com/amwolff/jellybot/internal/oshash"
)

// State is the identification state of a row.
type State int

// Identification states.
const (
	StatePending State = iota
	StateWorking
	StateIdentified
	StateAmbiguous
	StateUnmatched
	StateFailed
)

// Status is the state of a row with respect to its destination.
type Status int

// Destination states.
const (
	// StatusNone means that the row has no destination.
	StatusNone Status = iota
	// StatusReady means that the apply step can copy or move the row.
	StatusReady
	// StatusInPlace means that the destination is the source.
	StatusInPlace
	// StatusPresent means that the destination holds the same contents.
	StatusPresent
	// StatusConflict means that the apply step must not touch the row.
	StatusConflict
)

// Sidecar is a sidecar file of a row.
type Sidecar struct {
	// Rel is relative to the source root.
	Rel    string
	Suffix string
	Size   int64
}

// Row is one media file and its plan.
type Row struct {
	// Rel is relative to the source root.
	Rel      string
	Size     int64
	ModTime  time.Time
	Kind     media.Kind
	Sidecars []Sidecar

	State  State
	Stage  string
	Result ident.Result
	Err    error

	// Choices of the user.
	Skip         bool
	Unidentified bool
	// Choice indexes Result.Candidates; -1 means no choice.
	Choice int

	// Dest is relative to the destination root, or empty.
	Dest   string
	Status Status
	// Note explains StatusConflict and invalid destinations.
	Note string

	// Outcome of the apply step.
	Applied  bool
	ApplyErr error

	// The last disk check, for the destination diskDest.
	diskDest   string
	diskStatus Status
	diskNote   string
}

// Identity returns the identity that the row uses, or nil.
func (r *Row) Identity() *ident.Identity {
	switch r.State {
	case StateIdentified:
		return r.Result.Identity
	case StateAmbiguous:
		if r.Choice >= 0 && r.Choice < len(r.Result.Candidates) {
			return &r.Result.Candidates[r.Choice]
		}
	}
	return nil
}

// Included reports whether the apply step includes the row.
func (r *Row) Included() bool {
	return !r.Skip && !r.Applied && r.Status == StatusReady
}

// Busy reports whether the identification of the row is not complete.
func (r *Row) Busy() bool {
	return r.State == StatePending || r.State == StateWorking
}

// Text describes the destination side of the row for display.
func (r *Row) Text() string {
	switch {
	case r.Applied && r.ApplyErr != nil:
		return "✓ " + r.Dest + " (⚠ " + r.ApplyErr.Error() + ")"
	case r.Applied:
		return "✓ " + r.Dest
	case r.ApplyErr != nil:
		return "✗ " + r.ApplyErr.Error()
	case r.State == StatePending:
		return "waiting"
	case r.State == StateWorking:
		return r.Stage + "…"
	case r.State == StateFailed:
		return "✗ error: " + r.Err.Error()
	case r.State == StateAmbiguous && r.Identity() == nil:
		return fmt.Sprintf("⚠ ambiguous (%d candidates)", len(r.Result.Candidates))
	case r.State == StateUnmatched && !r.Unidentified:
		return "✗ unmatched: " + r.Result.Reason
	}

	switch r.Status {
	case StatusInPlace:
		return "= in place: " + r.Dest
	case StatusPresent:
		return "= already in library: " + r.Dest
	case StatusConflict:
		return "⚠ conflict: " + r.Note
	}
	if r.Unidentified {
		return "→ " + r.Dest
	}
	return r.Dest
}

// Counts summarizes a plan.
type Counts struct {
	Total, Busy, Matched, Ambiguous, Unmatched, Failed int
	Ready, Conflicts                                   int
	ReadyBytes                                         int64
}

// Plan holds the rows of a scan and computes their destinations.
type Plan struct {
	Opts   Options
	Layout jellyfin.Layout
	Gen    int
	Rows   []*Row
	Done   bool
	Err    error

	index map[string]*Row
	// dirty means that an event changed the plan after the last
	// Recompute.
	dirty bool
}

// NewPlan returns an empty plan for a scan of generation gen.
func NewPlan(opts Options, layout jellyfin.Layout, gen int) *Plan {
	return &Plan{Opts: opts, Layout: layout, Gen: gen, index: make(map[string]*Row)}
}

// Row returns the row at rel, or nil.
func (p *Plan) Row(rel string) *Row {
	p.Sync()
	return p.index[rel]
}

// Sync computes the destinations again if events changed the plan. Handle
// does not do this itself, because a large scan sends many events and each
// computation examines every row; Sync lets the owner do it once for many
// events (for example, once for each rendered frame).
func (p *Plan) Sync() {
	if p.dirty {
		p.Recompute()
	}
}

// Handle applies a scan event and reports whether it changed the plan.
// It ignores events of other generations.
func (p *Plan) Handle(e Event) bool {
	if e.generation() != p.Gen {
		return false
	}

	switch e := e.(type) {
	case Found:
		r := e.Row
		if old := p.index[r.Rel]; old != nil {
			return false
		}
		p.Rows = append(p.Rows, &r)
		p.index[r.Rel] = &r
		return true

	case Progress:
		r := p.index[e.Rel]
		if r == nil {
			return false
		}
		r.State, r.Stage = StateWorking, e.Stage
		return true

	case Identified:
		r := p.index[e.Rel]
		if r == nil {
			return false
		}
		r.Result, r.Err, r.Stage = e.Result, e.Err, ""
		switch {
		case e.Err != nil:
			r.State = StateFailed
		case e.Result.Identity != nil:
			r.State = StateIdentified
		case len(e.Result.Candidates) > 0:
			r.State = StateAmbiguous
			r.Choice = -1
		default:
			r.State = StateUnmatched
		}
		p.dirty = true
		return true

	case Done:
		p.Done, p.Err = true, e.Err
		return true
	}
	return false
}

// Counts summarizes the plan.
func (p *Plan) Counts() Counts {
	p.Sync()
	var c Counts
	c.Total = len(p.Rows)
	for _, r := range p.Rows {
		switch r.State {
		case StatePending, StateWorking:
			c.Busy++
		case StateIdentified:
			c.Matched++
		case StateAmbiguous:
			c.Ambiguous++
		case StateUnmatched:
			c.Unmatched++
		case StateFailed:
			c.Failed++
		}
		if r.Included() {
			c.Ready++
			c.ReadyBytes += r.Size
			for _, sc := range r.Sidecars {
				c.ReadyBytes += sc.Size
			}
		}
		if !r.Skip && r.Status == StatusConflict {
			c.Conflicts++
		}
	}
	return c
}

// Recompute computes the destination and the status of each row.
func (p *Plan) Recompute() {
	p.dirty = false
	for _, r := range p.Rows {
		if r.Applied {
			continue
		}
		r.Dest, r.Status, r.Note = "", StatusNone, ""
		switch id := r.Identity(); {
		case id != nil:
			dest, err := p.Layout.Path(*id, filepath.Ext(r.Rel))
			if err != nil {
				r.Status, r.Note = StatusConflict, err.Error()
				continue
			}
			r.Dest = dest
		case r.State == StateUnmatched && r.Unidentified:
			r.Dest = p.Layout.UnidentifiedPath(r.Rel)
		default:
			continue
		}
		r.Status = StatusReady
	}

	// Two rows (or sidecars) must not have the same destination.
	owners := make(map[string]*Row)
	for _, r := range p.Rows {
		if r.Skip || r.Applied || r.Status != StatusReady {
			continue
		}
		for _, d := range r.destinations() {
			if other := owners[d]; other != nil && other != r {
				other.Status, other.Note = StatusConflict, "same destination as "+r.Rel
				r.Status, r.Note = StatusConflict, "same destination as "+other.Rel
			}
			owners[d] = r
		}
	}

	for _, r := range p.Rows {
		if r.Applied || r.Status != StatusReady {
			continue
		}
		p.checkDisk(r)
	}
}

// destinations returns the destinations of the row and its sidecars,
// relative to the destination root.
func (r *Row) destinations() []string {
	out := []string{r.Dest}
	for _, sc := range r.Sidecars {
		out = append(out, jellyfin.SidecarPath(r.Dest, sc.Suffix))
	}
	return out
}

// InvalidateDisk makes the next Recompute examine the disk again, for
// example after the apply step.
func (p *Plan) InvalidateDisk() {
	for _, r := range p.Rows {
		r.diskDest = ""
	}
}

// checkDisk compares the destination of r with the files on disk. It
// examines the disk only when the destination changed.
func (p *Plan) checkDisk(r *Row) {
	if r.diskDest != r.Dest {
		p.examineDisk(r)
		r.diskDest, r.diskStatus, r.diskNote = r.Dest, r.Status, r.Note
		return
	}
	r.Status, r.Note = r.diskStatus, r.diskNote
}

func (p *Plan) examineDisk(r *Row) {
	src := filepath.Join(p.Opts.Source, r.Rel)
	dst := filepath.Join(p.Opts.Dest, r.Dest)
	if src == dst {
		r.Status = StatusInPlace
		return
	}

	fi, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		r.Status, r.Note = StatusConflict, err.Error()
		return
	default:
		same, err := sameContents(src, dst, r.Size, fi)
		switch {
		case err != nil:
			r.Status, r.Note = StatusConflict, err.Error()
		case same:
			r.Status = StatusPresent
		default:
			r.Status, r.Note = StatusConflict, "a different file is at "+r.Dest
		}
		return
	}

	for _, sc := range r.Sidecars {
		d := jellyfin.SidecarPath(r.Dest, sc.Suffix)
		if _, err := os.Lstat(filepath.Join(p.Opts.Dest, d)); !errors.Is(err, fs.ErrNotExist) {
			r.Status, r.Note = StatusConflict, "a file is at "+d
			return
		}
	}
}

// sameContents compares the file at src (of size bytes) with the file at
// dst by size and OpenSubtitles hash, or byte by byte for small files.
func sameContents(src, dst string, size int64, dstInfo fs.FileInfo) (bool, error) {
	if !dstInfo.Mode().IsRegular() || dstInfo.Size() != size {
		return false, nil
	}
	if size < oshash.MinSize {
		a, err := os.ReadFile(src) //nolint:gosec // comparing planned files is the purpose
		if err != nil {
			return false, err
		}
		b, err := os.ReadFile(dst) //nolint:gosec // comparing planned files is the purpose
		if err != nil {
			return false, err
		}
		return bytes.Equal(a, b), nil
	}
	ha, _, err := oshash.File(src)
	if err != nil {
		return false, err
	}
	hb, _, err := oshash.File(dst)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// Op is one file operation of the apply step.
type Op struct {
	Row *Row
	// Rel is the source path of Row, relative to the source root.
	Rel string
	// Src and Dst are absolute.
	Src, Dst string
	Size     int64
	// Sidecar is true for a sidecar of Row.
	Sidecar bool
}

// Ops returns the operations for the included rows: each main file, then
// its sidecars.
func (p *Plan) Ops() []Op {
	p.Sync()
	var ops []Op
	for _, r := range p.Rows {
		if !r.Included() {
			continue
		}
		ops = append(ops, Op{
			Row:  r,
			Rel:  r.Rel,
			Src:  filepath.Join(p.Opts.Source, r.Rel),
			Dst:  filepath.Join(p.Opts.Dest, r.Dest),
			Size: r.Size,
		})
		for _, sc := range r.Sidecars {
			ops = append(ops, Op{
				Row:     r,
				Rel:     r.Rel,
				Src:     filepath.Join(p.Opts.Source, sc.Rel),
				Dst:     filepath.Join(p.Opts.Dest, jellyfin.SidecarPath(r.Dest, sc.Suffix)),
				Size:    sc.Size,
				Sidecar: true,
			})
		}
	}
	return ops
}

// WriteText writes the plan as "source → destination" lines and a summary.
func (p *Plan) WriteText(w io.Writer) error {
	p.Sync()
	for _, r := range p.Rows {
		if _, err := fmt.Fprintf(w, "%s → %s\n", r.Rel, r.Text()); err != nil {
			return err
		}
		if id := r.Identity(); id != nil && id.Detail != "" {
			if _, err := fmt.Fprintf(w, "    [%s]\n", id.Detail); err != nil {
				return err
			}
		}
		for i, c := range r.Result.Candidates {
			if r.Identity() != nil {
				break
			}
			if _, err := fmt.Fprintf(w, "    candidate %d: %s (%d) [%s]\n", i+1, c.Title, c.Year, c.Detail); err != nil {
				return err
			}
		}
	}
	c := p.Counts()
	_, err := fmt.Fprintf(w, "\n%d files: %d matched, %d ambiguous, %d unmatched, %d failed, %d conflicts; %d ready (%s)\n",
		c.Total, c.Matched, c.Ambiguous, c.Unmatched, c.Failed, c.Conflicts, c.Ready, HumanBytes(c.ReadyBytes))
	return err
}

// HumanBytes formats n bytes with a binary unit.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
