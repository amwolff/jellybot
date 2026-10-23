package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/amwolff/bits/progutil"
	"github.com/zeebo/clingy"

	"github.com/amwolff/jellybot/internal/engine"
	"github.com/amwolff/jellybot/internal/tui"
)

type cmdPlan struct {
	g   progutil.Global
	cfg config
}

func (c *cmdPlan) Setup(params clingy.Parameters) { c.cfg.setup(params) }

func (c *cmdPlan) Execute(ctx context.Context) error {
	a, err := c.cfg.open(c.g.Log)
	if err != nil {
		return err
	}
	defer func() { _ = a.store.Close() }()

	stdout, stderr := clingy.Stdout(ctx), clingy.Stderr(ctx)
	showProgress := isTerminal(stderr)

	p := engine.NewPlan(a.opts, c.cfg.layout, 1)
	var mu sync.Mutex
	a.scanner.Scan(ctx, 1, a.opts, func(e engine.Event) {
		mu.Lock()
		defer mu.Unlock()
		p.Handle(e)
		if _, ok := e.(engine.Identified); ok && showProgress {
			cnt := p.Counts()
			_, _ = fmt.Fprintf(stderr, "\ridentified %d/%d", cnt.Total-cnt.Busy, cnt.Total)
		}
	})
	if showProgress {
		_, _ = fmt.Fprint(stderr, "\r\033[K")
	}
	if p.Err != nil {
		return p.Err
	}

	if _, err := fmt.Fprintf(stdout, "providers: %s\n\n", a.providers); err != nil {
		return err
	}
	return p.WriteText(stdout)
}

type cmdTUI struct {
	g   progutil.Global
	cfg config
}

func (c *cmdTUI) Setup(params clingy.Parameters) { c.cfg.setup(params) }

func (c *cmdTUI) Execute(ctx context.Context) error {
	// The progutil logger writes to stdout, which the TUI owns.
	log := slog.New(slog.DiscardHandler)
	if c.cfg.logFile != "" {
		f, err := os.OpenFile(c.cfg.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		level := slog.LevelInfo
		if c.g.Log.Enabled(ctx, slog.LevelDebug) {
			level = slog.LevelDebug
		}
		log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	}

	a, err := c.cfg.open(log)
	if err != nil {
		return err
	}
	defer func() { _ = a.store.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := tui.New(ctx, tui.Config{
		Scanner:   a.scanner,
		Store:     a.store,
		Opts:      a.opts,
		Layout:    c.cfg.layout,
		Mode:      a.mode,
		Modes:     c.cfg.modes,
		Owner:     c.cfg.owner,
		Providers: a.providers,
		Log:       log,
	})
	prog := tea.NewProgram(m, tea.WithContext(ctx))
	m.SetSender(prog.Send)
	_, err = prog.Run()
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	return err
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
