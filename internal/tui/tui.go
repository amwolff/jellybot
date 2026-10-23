// Package tui is the two-pane jellybot interface: source files on the left,
// their destinations on the right, aligned row by row.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amwolff/jellybot/internal/engine"
	"github.com/amwolff/jellybot/internal/fsop"
	"github.com/amwolff/jellybot/internal/jellyfin"
	"github.com/amwolff/jellybot/internal/store"
)

// Config configures the model.
type Config struct {
	Scanner   *engine.Scanner
	Store     *store.Store
	Opts      engine.Options
	Layout    jellyfin.Layout
	Mode      fsop.Mode
	Modes     fsop.Modes
	Owner     fsop.Owner
	Providers string
	Log       *slog.Logger
}

type (
	engineMsg   struct{ e engine.Event }
	opDoneMsg   struct{ d engine.OpDone }
	progressMsg struct{ p engine.ApplyProgress }
	applyEndMsg struct{}
	errMsg      struct{ err error }
)

type keyMap struct {
	Up, Down, PageUp, PageDown, Home, End key.Binding
	Skip, Mode, Recursive, Unidentified   key.Binding
	Candidate, Refresh, Apply, Quit       key.Binding
	Yes, No                               key.Binding
}

func newKeyMap() keyMap {
	b := func(help string, desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return keyMap{
		Up:           b("↑/k", "up", "up", "k"),
		Down:         b("↓/j", "down", "down", "j"),
		PageUp:       b("pgup", "page up", "pgup"),
		PageDown:     b("pgdn", "page down", "pgdown"),
		Home:         b("home/g", "first", "home", "g"),
		End:          b("end/G", "last", "end", "G", "shift+g"),
		Skip:         b("space", "skip", "space"),
		Mode:         b("m", "mode", "m"),
		Recursive:    b("r", "recursive", "r"),
		Unidentified: b("u", "unidentified", "u"),
		Candidate:    b("c", "candidate", "c"),
		Refresh:      b("R", "refresh", "R", "shift+r"),
		Apply:        b("a", "apply", "a"),
		Quit:         b("q", "quit", "q", "ctrl+c"),
		Yes:          b("y", "yes", "y", "Y"),
		No:           b("n", "no", "n", "N", "esc"),
	}
}

func (k keyMap) short() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Skip, k.Mode, k.Recursive, k.Unidentified, k.Candidate, k.Refresh, k.Apply, k.Quit}
}

// prefs are the choices of the user for a row. They survive a new scan.
type prefs struct {
	skip, unidentified bool
}

// Model is the Bubble Tea model.
type Model struct {
	cfg  Config
	send func(tea.Msg)
	ctx  context.Context

	gen        int
	cancelScan context.CancelFunc
	plan       *engine.Plan
	prefs      map[string]prefs

	width, height  int
	cursor, offset int
	recursive      bool
	mode           fsop.Mode

	confirming   bool
	applying     bool
	cancelApply  context.CancelFunc
	applied      engine.ApplyProgress
	quitAfter    bool
	applyResults [2]int // successes, failures
	message      string

	spinner  spinner.Model
	progress progress.Model
	help     help.Model
	keys     keyMap
}

// New returns a model. Call SetSender before the program runs.
func New(ctx context.Context, cfg Config) *Model {
	m := &Model{
		cfg:       cfg,
		ctx:       ctx,
		prefs:     make(map[string]prefs),
		recursive: cfg.Opts.Recursive,
		mode:      cfg.Mode,
		spinner:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		progress:  progress.New(progress.WithDefaultBlend()),
		help:      help.New(),
		keys:      newKeyMap(),
		width:     80,
		height:    24,
	}
	m.plan = engine.NewPlan(cfg.Opts, cfg.Layout, 0)
	return m
}

// SetSender sets the function that delivers messages from other
// goroutines, normally (*tea.Program).Send.
func (m *Model) SetSender(send func(tea.Msg)) {
	m.send = send
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	m.startScan()
	return m.spinner.Tick
}

func (m *Model) startScan() {
	if m.cancelScan != nil {
		m.cancelScan()
	}
	m.gen++
	opts := m.cfg.Opts
	opts.Recursive = m.recursive
	m.plan = engine.NewPlan(opts, m.cfg.Layout, m.gen)
	m.cursor, m.offset = 0, 0

	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelScan = cancel
	gen, send := m.gen, m.send
	go m.cfg.Scanner.Scan(ctx, gen, opts, func(e engine.Event) { send(engineMsg{e}) })
}

func (m *Model) refresh(r *engine.Row) {
	if r.Busy() || r.Applied {
		return
	}
	r.State, r.Stage = engine.StateWorking, "refreshing"
	ctx := store.WithRefresh(m.ctx)
	gen, opts, row, send := m.gen, m.plan.Opts, *r, m.send
	go m.cfg.Scanner.Identify(ctx, gen, opts, row, func(e engine.Event) { send(engineMsg{e}) })
}

func (m *Model) current() *engine.Row {
	if m.cursor >= 0 && m.cursor < len(m.plan.Rows) {
		return m.plan.Rows[m.cursor]
	}
	return nil
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.progress.SetWidth(max(msg.Width-4, 10))
		m.help.SetWidth(msg.Width)
		m.clampScroll()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)
		return m, cmd

	case engineMsg:
		if f, ok := msg.e.(engine.Found); ok {
			if p, ok := m.prefs[f.Row.Rel]; ok {
				f.Row.Skip, f.Row.Unidentified = p.skip, p.unidentified
				msg.e = f
			}
		}
		m.plan.Handle(msg.e)
		if d, ok := msg.e.(engine.Done); ok && d.Err != nil && d.Err != context.Canceled {
			m.message = "scan failed: " + d.Err.Error()
		}
		return m, nil

	case progressMsg:
		m.applied = msg.p
		if msg.p.Total > 0 {
			return m, m.progress.SetPercent(float64(msg.p.Done) / float64(msg.p.Total))
		}
		return m, nil

	case opDoneMsg:
		m.plan.Applied(msg.d)
		if !msg.d.Sidecar {
			if msg.d.Err == nil {
				m.applyResults[0]++
			} else {
				m.applyResults[1]++
			}
		}
		return m, nil

	case applyEndMsg:
		m.applying = false
		m.cancelApply = nil
		m.plan.InvalidateDisk()
		m.plan.Recompute()
		m.message = fmt.Sprintf("%s: %d done, %d failed", m.mode, m.applyResults[0], m.applyResults[1])
		if m.quitAfter {
			return m, tea.Quit
		}
		return m, nil

	case errMsg:
		m.message = msg.err.Error()
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.applying {
		if msg.String() == "ctrl+c" {
			m.quitAfter = true
			m.message = "stopping after the current file…"
			if m.cancelApply != nil {
				m.cancelApply()
			}
		}
		return m, nil
	}

	if m.confirming && msg.String() == "ctrl+c" {
		m.confirming = false
		m.cancelScan()
		return m, tea.Quit
	}

	if m.confirming {
		switch {
		case key.Matches(msg, m.keys.Yes):
			m.confirming = false
			return m, m.startApply()
		case key.Matches(msg, m.keys.No), key.Matches(msg, m.keys.Quit):
			m.confirming = false
		}
		return m, nil
	}

	m.message = ""
	m.plan.Sync()
	r := m.current()
	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.cancelScan != nil {
			m.cancelScan()
		}
		return m, tea.Quit
	case key.Matches(msg, m.keys.Up):
		m.move(-1)
	case key.Matches(msg, m.keys.Down):
		m.move(1)
	case key.Matches(msg, m.keys.PageUp):
		m.move(-m.bodyHeight())
	case key.Matches(msg, m.keys.PageDown):
		m.move(m.bodyHeight())
	case key.Matches(msg, m.keys.Home):
		m.move(-len(m.plan.Rows))
	case key.Matches(msg, m.keys.End):
		m.move(len(m.plan.Rows))
	case key.Matches(msg, m.keys.Mode):
		if m.mode == fsop.Copy {
			m.mode = fsop.Move
		} else {
			m.mode = fsop.Copy
		}
	case key.Matches(msg, m.keys.Recursive):
		m.recursive = !m.recursive
		m.startScan()
	case r == nil:
	case key.Matches(msg, m.keys.Skip):
		if !r.Applied {
			r.Skip = !r.Skip
			m.savePrefs(r)
			m.plan.Recompute()
		}
	case key.Matches(msg, m.keys.Unidentified):
		if r.State == engine.StateUnmatched && !r.Applied {
			r.Unidentified = !r.Unidentified
			m.savePrefs(r)
			m.plan.Recompute()
		} else {
			m.message = "only unmatched files can go to " + m.cfg.Layout.Unidentified
		}
	case key.Matches(msg, m.keys.Candidate):
		if r.State != engine.StateAmbiguous || r.Applied {
			m.message = "only ambiguous files have candidates"
			break
		}
		r.Choice = (r.Choice + 1) % len(r.Result.Candidates)
		m.plan.Recompute()
		if r.Result.HasKey {
			k, cand := r.Result.Key, r.Result.Candidates[r.Choice]
			return m, func() tea.Msg {
				if err := m.cfg.Scanner.Identifier.Choose(k, cand); err != nil {
					return errMsg{err}
				}
				return nil
			}
		}
	case key.Matches(msg, m.keys.Refresh):
		m.refresh(r)
	}

	if key.Matches(msg, m.keys.Apply) {
		c := m.plan.Counts()
		if c.Ready == 0 {
			m.message = "nothing to apply"
		} else {
			m.confirming = true
		}
	}
	return m, nil
}

func (m *Model) savePrefs(r *engine.Row) {
	m.prefs[r.Rel] = prefs{skip: r.Skip, unidentified: r.Unidentified}
}

func (m *Model) startApply() tea.Cmd {
	ops := m.plan.Ops()
	if len(ops) == 0 {
		return nil
	}
	m.applying = true
	m.applyResults = [2]int{}
	m.applied = engine.ApplyProgress{}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelApply = cancel

	applier := &fsop.Applier{Mode: m.mode, Modes: m.cfg.Modes, Owner: m.cfg.Owner}
	send, st := m.send, m.cfg.Store
	go func() {
		defer send(applyEndMsg{})
		engine.Run(ctx, applier, st, ops,
			func(p engine.ApplyProgress) { send(progressMsg{p}) },
			func(d engine.OpDone) { send(opDoneMsg{d}) })
	}()
	return m.progress.SetPercent(0)
}

func (m *Model) move(delta int) {
	if len(m.plan.Rows) == 0 {
		return
	}
	m.cursor = min(max(m.cursor+delta, 0), len(m.plan.Rows)-1)
	m.clampScroll()
}

// Layout: title, header, rule, body, rule, detail, status, help or prompt.
const chromeHeight = 7

func (m *Model) bodyHeight() int {
	h := m.height - chromeHeight
	if m.applying {
		h--
	}
	return max(h, 1)
}

// lines returns the display lines of the rows: each row, then its
// sidecars. It returns the index of the first line of each row.
func (m *Model) lineStarts() []int {
	starts := make([]int, len(m.plan.Rows))
	n := 0
	for i, r := range m.plan.Rows {
		starts[i] = n
		n += 1 + len(r.Sidecars)
	}
	return starts
}

func (m *Model) clampScroll() {
	if len(m.plan.Rows) == 0 {
		m.offset = 0
		return
	}
	starts := m.lineStarts()
	first := starts[m.cursor]
	last := first + len(m.plan.Rows[m.cursor].Sidecars)
	h := m.bodyHeight()
	if first < m.offset {
		m.offset = first
	}
	if last >= m.offset+h {
		m.offset = last - h + 1
	}
	m.offset = max(m.offset, 0)
}

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleHeader   = lipgloss.NewStyle().Bold(true).Underline(true)
	styleRule     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleCursor   = lipgloss.NewStyle().Reverse(true)
	styleFaint    = lipgloss.NewStyle().Faint(true)
	styleSkipped  = lipgloss.NewStyle().Faint(true).Strikethrough(true)
	styleBad      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleGood     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleMove     = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleProvider = lipgloss.NewStyle().Faint(true)
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "jellybot"
	return v
}

func (m *Model) render() string {
	m.plan.Sync()
	w := max(m.width, 20)
	lw := (w - 3) / 2
	rw := w - lw - 3
	sep := styleRule.Render(" │ ")

	var b strings.Builder
	line := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}

	line(styleTitle.Render("jellybot") + "  " + styleProvider.Render(truncate(m.cfg.Providers, w-10)))
	line(styleHeader.Render(pad(truncateMiddle("SRC "+m.plan.Opts.Source, lw), lw)) + sep +
		styleHeader.Render(pad(truncateMiddle("DST "+m.plan.Opts.Dest, rw), rw)))
	line(styleRule.Render(strings.Repeat("─", lw+1) + "┼" + strings.Repeat("─", rw+1)))

	// Body.
	body := make([]string, 0, m.bodyHeight())
	lineNo := 0
	for i, r := range m.plan.Rows {
		if lineNo >= m.offset+m.bodyHeight() {
			break
		}
		if lineNo+1+len(r.Sidecars) <= m.offset {
			lineNo += 1 + len(r.Sidecars)
			continue
		}
		left, right := m.rowText(r, lw, rw)
		if lineNo >= m.offset {
			if i == m.cursor {
				left = styleCursor.Render(pad(ansi.Strip(left), lw))
			}
			body = append(body, pad(left, lw)+sep+pad(right, rw))
		}
		lineNo++
		for _, sc := range r.Sidecars {
			if lineNo >= m.offset && lineNo < m.offset+m.bodyHeight() {
				l := "  ↳ " + truncateMiddle(filepath.Base(sc.Rel), lw-4)
				rt := ""
				if r.Dest != "" {
					rt = "  ↳ " + truncateMiddle(filepath.Base(jellyfin.SidecarPath(r.Dest, sc.Suffix)), rw-4)
				}
				body = append(body, styleFaint.Render(pad(l, lw))+sep+styleFaint.Render(pad(rt, rw)))
			}
			lineNo++
		}
	}
	if len(m.plan.Rows) == 0 {
		msg := "no media files"
		if !m.plan.Done {
			msg = m.spinner.View() + " scanning…"
		}
		body = append(body, pad(msg, lw)+sep)
	}
	for len(body) < m.bodyHeight() {
		body = append(body, strings.Repeat(" ", lw)+sep)
	}
	for _, l := range body {
		line(l)
	}

	line(styleRule.Render(strings.Repeat("─", lw+1) + "┴" + strings.Repeat("─", rw+1)))
	line(truncate(m.detail(), w))
	line(truncate(m.status(), w))

	switch {
	case m.applying:
		line(m.progress.View())
		b.WriteString(truncate(fmt.Sprintf("%s %s / %s  %s", m.spinner.View(),
			engine.HumanBytes(m.applied.Done), engine.HumanBytes(m.applied.Total), m.applied.Current), w))
	case m.confirming:
		c := m.plan.Counts()
		verb := "Copy"
		if m.mode == fsop.Move {
			verb = "Move"
		}
		b.WriteString(styleWarn.Render(fmt.Sprintf("%s %d files (%s) to %s? y/n", verb, c.Ready,
			engine.HumanBytes(c.ReadyBytes), m.plan.Opts.Dest)))
	case m.message != "":
		b.WriteString(styleWarn.Render(truncate(m.message, w)))
	default:
		b.WriteString(m.help.ShortHelpView(m.keys.short()))
	}
	return b.String()
}

func (m *Model) rowText(r *engine.Row, lw, rw int) (string, string) {
	left := " " + truncateMiddle(r.Rel, lw-1)
	right := r.Text()
	if r.Busy() {
		right = m.spinner.View() + " " + right
	}
	right = truncateMiddle(right, rw)

	switch {
	case r.Skip:
		return styleSkipped.Render(left), styleSkipped.Render(right)
	case r.Applied && r.ApplyErr == nil:
		return styleFaint.Render(left), styleGood.Render(right)
	case r.ApplyErr != nil, r.State == engine.StateFailed:
		return left, styleBad.Render(right)
	case r.Busy():
		return left, styleFaint.Render(right)
	case r.State == engine.StateUnmatched && !r.Unidentified:
		return left, styleBad.Render(right)
	case r.Status == engine.StatusConflict, r.State == engine.StateAmbiguous && r.Identity() == nil:
		return left, styleWarn.Render(right)
	case r.Status == engine.StatusInPlace, r.Status == engine.StatusPresent:
		return styleFaint.Render(left), styleFaint.Render(right)
	case r.Unidentified:
		return left, styleMove.Render(right)
	}
	return left, right
}

func (m *Model) detail() string {
	r := m.current()
	if r == nil {
		return ""
	}
	// The header shows the roots; the paths here are relative to them.
	src := r.Rel
	switch {
	case r.Dest != "":
		s := src + " → " + r.Dest
		if id := r.Identity(); id != nil && id.Detail != "" {
			s = "[" + id.Detail + "] " + s
		}
		if r.State == engine.StateAmbiguous {
			s = fmt.Sprintf("candidate %d/%d: %s", r.Choice+1, len(r.Result.Candidates), s)
		}
		return s
	case r.State == engine.StateAmbiguous:
		var names []string
		for _, c := range r.Result.Candidates {
			names = append(names, fmt.Sprintf("%s (%d)", c.Title, c.Year))
		}
		return src + "  candidates: " + strings.Join(names, ", ") + " (press c)"
	case r.State == engine.StateUnmatched:
		return src + "  (press u to send it to " + m.cfg.Layout.Unidentified + ")"
	}
	return src
}

func (m *Model) status() string {
	c := m.plan.Counts()
	parts := []string{
		"mode: " + strings.ToUpper(m.mode.String()),
		"recursive: " + map[bool]string{true: "on", false: "off"}[m.recursive],
	}
	if s := m.cfg.Modes.String(); s != "" {
		parts = append(parts, "chmod "+s)
	}
	if s := m.cfg.Owner.String(); s != "" {
		parts = append(parts, "chown "+s)
	}
	counts := fmt.Sprintf("%d/%d matched", c.Matched, c.Total)
	if c.Busy > 0 {
		counts += fmt.Sprintf(", %d busy", c.Busy)
	}
	if c.Ambiguous > 0 {
		counts += fmt.Sprintf(", %d ambiguous", c.Ambiguous)
	}
	if c.Unmatched > 0 {
		counts += fmt.Sprintf(", %d unmatched", c.Unmatched)
	}
	if c.Failed > 0 {
		counts += fmt.Sprintf(", %d failed", c.Failed)
	}
	if c.Conflicts > 0 {
		counts += fmt.Sprintf(", %d conflicts", c.Conflicts)
	}
	parts = append(parts, counts, fmt.Sprintf("ready: %d (%s)", c.Ready, engine.HumanBytes(c.ReadyBytes)))
	return " " + strings.Join(parts, " · ")
}

// pad pads s with spaces to width w (in terminal cells).
func pad(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func truncate(s string, w int) string {
	return ansi.Truncate(s, max(w, 0), "…")
}

// truncateMiddle shortens s to w cells with an ellipsis in the middle, so
// that the start and the end of a path stay visible.
func truncateMiddle(s string, w int) string {
	n := ansi.StringWidth(s)
	if n <= w {
		return s
	}
	if w <= 1 {
		return ansi.Truncate(s, max(w, 0), "")
	}
	head := (w - 1) / 2
	tail := w - 1 - head
	end := ansi.TruncateLeft(s, n-tail, "")
	// A cut inside a wide character can leave one cell too many.
	for ansi.StringWidth(end) > tail {
		_, size := utf8.DecodeRuneInString(end)
		end = end[size:]
	}
	return ansi.Truncate(s, head, "") + "…" + end
}
