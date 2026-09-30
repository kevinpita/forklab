package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSmokeEverySizeAndTheme drives every panel and overlay at every theme
// and size, down to 12x8, and checks each frame fits the terminal exactly.
func TestSmokeEverySizeAndTheme(t *testing.T) {
	sizes := [][2]int{{180, 50}, {120, 40}, {80, 24}, {70, 22}, {59, 20}, {40, 12}, {20, 6}, {12, 8}, {1, 1}}
	walk := []string{
		"?", "j", "esc", "ctrl+k", "n", "o", "d", "down", "esc", ":", "n", "space", "l", "esc",
		"c", "j", "esc", "s", "esc", "K", "esc", "l", "w", "enter", "k", "G", "pgdown", "esc",
	}
	for p := range numPanels {
		walk = append(walk, fmt.Sprint(int(p)+1), "j", "G", "g", "enter", "j", "g", "esc", "?", "esc", "c", "esc")
	}
	walk = append(walk, "tab", "shift+tab", "T", "T", "T", "6", "u", "esc", "d", "esc")
	for _, name := range themeNames() {
		for _, dark := range []bool{true, false} {
			for _, size := range sizes {
				m := loadedModel(t, buildTheme(name, dark))
				m.Update(windowSize(size))
				mustFit(t, m, name, size, "start")
				for _, k := range walk {
					press(m, k)
					mustFit(t, m, name, size, k)
				}
				m.output = &Result{Cmd: Command{"exec", "--", "q", "bank", "total"}, Data: testdata(t, "exec_ok.json")}
				mustFit(t, m, name, size, "output")
				multi := errors.New("rpc status: failed\n\tcaused by: dial tcp\nconnection refused")
				m.output, m.last = nil, &Result{Cmd: Command{"node", "list"}, Err: multi}
				m.setStatus(nil)
				m.streams[streamStatus].err, m.loads[loadNodes].err = multi, multi
				for _, k := range []string{"1", "2", "5"} {
					press(m, k)
					mustFit(t, m, name, size, "multi-line error "+k)
				}
			}
		}
	}
}

func mustFit(t *testing.T, m *Model, theme string, size [2]int, after string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic theme=%s size=%v after %q: %v", theme, size, after, r)
		}
	}()
	out := m.View().Content
	if out == "" {
		t.Fatalf("empty frame theme=%s size=%v after %q", theme, size, after)
	}
	lines := strings.Split(out, "\n")
	if len(lines) > size[1] {
		t.Fatalf("frame is %d lines, terminal %d (theme=%s size=%v after %q)", len(lines), size[1], theme, size, after)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > size[0] {
			t.Fatalf("line %d cells wide, terminal %d (theme=%s size=%v after %q):\n%s", w, size[0], theme, size, after, ansi.Strip(out))
		}
	}
}
