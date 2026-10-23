package tui

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"go.senan.xyz/taglib"

	"github.com/amwolff/jellybot/internal/engine"
	"github.com/amwolff/jellybot/internal/fsop"
	"github.com/amwolff/jellybot/internal/ident"
	"github.com/amwolff/jellybot/internal/jellyfin"
	"github.com/amwolff/jellybot/internal/provider/providertest"
)

func writeWAV(t *testing.T, path string) {
	t.Helper()
	const rate = 8000
	const size = 10 * rate * 2
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, 36+size)
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, rate)
	b = binary.LittleEndian.AppendUint32(b, rate*2)
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, size)
	b = append(b, data...)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

type harness struct {
	t  *testing.T
	m  *Model
	ch chan tea.Msg
}

func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		default:
			msg = tea.KeyPressMsg{Code: []rune(strings.ToLower(k))[0], Text: k}
		}
		h.m.Update(msg)
	}
}

// pump delivers messages from other goroutines until cond holds.
func (h *harness) pump(cond func() bool) {
	h.t.Helper()
	deadline := time.After(20 * time.Second)
	for !cond() {
		select {
		case msg := <-h.ch:
			h.m.Update(msg)
		case <-deadline:
			h.t.Fatalf("timeout; view:\n%s", h.view())
		}
	}
}

func (h *harness) view() string {
	return ansi.Strip(h.m.View().Content)
}

func TestModel(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeWAV(t, filepath.Join(src, "banger lol.wav"))
	if err := taglib.WriteTags(filepath.Join(src, "banger lol.wav"), map[string][]string{
		taglib.Artist: {"Daft Punk"}, taglib.Album: {"Discovery"}, taglib.Title: {"One More Time"},
		taglib.TrackNumber: {"1"}, taglib.Date: {"2001"},
	}, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "banger lol.lrc"), []byte("[00:01.00]One more time"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWAV(t, filepath.Join(src, "mystery noise.wav"))
	if err := os.MkdirAll(filepath.Join(src, "deeper"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeWAV(t, filepath.Join(src, "deeper", "more noise.wav"))

	s := providertest.Store(t)
	m := New(context.Background(), Config{
		Scanner: &engine.Scanner{Identifier: &ident.Identifier{Store: s}},
		Store:   s,
		Opts:    engine.Options{Source: src, Dest: dst, Jobs: 2},
		Layout:  jellyfin.DefaultLayout,
		Mode:    fsop.Copy,
		Modes:   fsop.Modes{File: 0o640, HasFile: true},
	})
	h := &harness{t: t, m: m, ch: make(chan tea.Msg, 1024)}
	m.SetSender(func(msg tea.Msg) { h.ch <- msg })

	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 220, Height: 20})
	idle := func() bool { return m.plan.Done && m.plan.Counts().Busy == 0 }
	h.pump(idle)

	v := h.view()
	for _, want := range []string{
		"banger lol.wav",
		filepath.FromSlash("Music/Daft Punk/Discovery (2001)/01 - One More Time.wav"),
		"↳ banger lol.lrc",
		"↳ 01 - One More Time.lrc",
		"✗ unmatched: incomplete tags; install fpcalc for fingerprints",
		"mode: COPY",
		"recursive: off",
		"chmod F0640",
		"1/2 matched",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "more noise.wav") {
		t.Fatalf("non-recursive view shows a nested file:\n%s", v)
	}

	// Send the unmatched file to Unidentified.
	h.press("j", "u")
	if r := m.current(); r.Rel != "mystery noise.wav" || !r.Unidentified || r.Dest != filepath.Join("Unidentified", "mystery noise.wav") {
		t.Fatalf("row = %+v", r)
	}

	// Apply asks first; "n" cancels.
	h.press("a")
	if v = h.view(); !strings.Contains(v, "Copy 2 files") || !strings.Contains(v, "? y/n") {
		t.Fatalf("no confirmation:\n%s", v)
	}
	h.press("n")
	if m.confirming || m.applying {
		t.Fatal("still confirming after n")
	}

	h.press("a", "y")
	if !m.applying {
		t.Fatal("not applying after y")
	}
	h.pump(func() bool { return !m.applying })

	if v = h.view(); !strings.Contains(v, "copy: 2 done, 0 failed") {
		t.Fatalf("no result message:\n%s", v)
	}
	for rel, dest := range map[string]string{
		"banger lol.wav":    "Music/Daft Punk/Discovery (2001)/01 - One More Time.wav",
		"banger lol.lrc":    "Music/Daft Punk/Discovery (2001)/01 - One More Time.lrc",
		"mystery noise.wav": "Unidentified/mystery noise.wav",
	} {
		want, _ := os.ReadFile(filepath.Join(src, rel))                       //nolint:gosec // test paths
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(dest))) //nolint:gosec // test paths
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v", dest, err)
		}
		fi, _ := os.Stat(filepath.Join(dst, filepath.FromSlash(dest)))
		if fi.Mode().Perm() != 0o640 {
			t.Fatalf("%s mode = %v", dest, fi.Mode().Perm())
		}
	}
	if c := m.plan.Counts(); c.Ready != 0 {
		t.Fatalf("ready after apply = %d", c.Ready)
	}

	// The mode key switches to move; the recursive key scans again and
	// keeps the choices of the user.
	h.press("m", "r")
	h.pump(idle)
	v = h.view()
	for _, want := range []string{"mode: MOVE", "recursive: on", filepath.Join("deeper", "more noise.wav")} {
		if !strings.Contains(v, want) {
			t.Fatalf("view lacks %q:\n%s", want, v)
		}
	}
	// The copies are in the library now, so the rescan finds them there.
	if r := m.plan.Row("banger lol.wav"); r.Status != engine.StatusPresent {
		t.Fatalf("banger after rescan = %v %q", r.Status, r.Note)
	}
	if r := m.plan.Row("mystery noise.wav"); !r.Unidentified || r.Status != engine.StatusPresent {
		t.Fatalf("mystery after rescan = %+v", r)
	}

	// Skip a row with space.
	h.press("G")
	r := m.current()
	h.press("space")
	if r.Rel != "mystery noise.wav" || !r.Skip {
		t.Fatalf("skip: %+v", r)
	}
	h.press("space")
	if r.Skip {
		t.Fatal("space did not toggle skip back")
	}

	// q quits.
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
}

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"Movies/The Matrix (1999)/The Matrix.mkv", 15, "Movies/…rix.mkv"},
		{"abcdef", 1, "a"},
		{"日本語のファイル名.mkv", 10, "日本….mkv"},
	}
	for _, tt := range tests {
		got := truncateMiddle(tt.in, tt.w)
		if got != tt.want {
			t.Errorf("truncateMiddle(%q, %d) = %q, want %q", tt.in, tt.w, got, tt.want)
		}
		if ansi.StringWidth(got) > tt.w {
			t.Errorf("truncateMiddle(%q, %d) is %d cells wide", tt.in, tt.w, ansi.StringWidth(got))
		}
	}
}
