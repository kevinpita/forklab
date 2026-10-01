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

// TestSmokeForms opens every form at every size, changes a field, cycles
// a choice, and submits it half filled.
func TestSmokeForms(t *testing.T) {
	sizes := [][2]int{{180, 50}, {120, 40}, {80, 24}, {59, 20}, {40, 12}, {20, 6}, {12, 8}, {1, 1}}
	var walk []string
	for _, open := range [][]string{
		{"6", "n"},
		{"6", "R"},
		{"6", "m"},
		{"4", "u"},
		{"4", "X"},
		{"5", "s"},
		{"3", "n"},
		{"3", "v"},
		{"7", "n"},
		{"7", "e"},
		{"8", "f"},
		{"8", "b"},
		{"1", "R"},
		{"1", "x"},
	} {
		walk = append(walk, open...)
		walk = append(walk, "a", "tab", "right", "tab", "space", "enter", "shift+tab", "esc", "esc")
	}
	for _, th := range []Theme{buildTheme("ansi", true), buildTheme("gruvbox", false)} {
		for _, size := range sizes {
			m := loadedModel(t, th)
			m.upgrade.Plan = &chainPlan{Name: "v2", Height: 40}
			m.Update(windowSize(size))
			for _, k := range walk {
				press(m, k)
				mustFit(t, m, th.Name, size, k)
			}
		}
	}
}

// The focused panel lists every row it has while the screen has room,
// however many rows the other panels want.
func TestFocusedPanelListsEveryRow(t *testing.T) {
	for h := 14; h <= 44; h++ {
		size := [2]int{100, h}
		m := loadedModel(t, buildTheme("ansi", true))
		m.Update(windowSize(size))
		press(m, "4")
		lines := strings.Split(ansi.Strip(m.render()), "\n")
		box := ""
		for i, l := range lines {
			if strings.Contains(l, "[4] Upgrades") {
				for _, row := range lines[i+1:] {
					left := string([]rune(row)[:m.leftW()])
					if strings.Contains(left, "╰") {
						break
					}
					box += left + "\n"
				}
			}
		}
		if !strings.Contains(box, "node0") || !strings.Contains(box, "node1") {
			t.Errorf("size %v: focused Upgrades lists\n%s", size, box)
		}
	}
}

// TestSmokeFirstLabWizard walks the wizard every step at every size.
func TestSmokeFirstLabWizard(t *testing.T) {
	sizes := [][2]int{{180, 50}, {80, 24}, {59, 20}, {40, 12}, {20, 6}, {12, 8}, {1, 1}}
	for _, size := range sizes {
		m := emptyModel(t)
		m.Update(windowSize(size))
		feedLabs(t, m, `[]`)
		m.form.loads[Command{"profile", "list"}.String()] = &optionLoad{done: true, data: []byte(`[{"name":"simd","origin":"builtin"}]`)}
		m.form.loads[Command{"profile", "show", "simd"}.String()] = &optionLoad{done: true, data: []byte(`{"profile":{"chain_id":"simd-1","binaries":{"0.53.8":{"url":"https://example.invalid/simd"}}}}`)}
		m.syncForm()
		mustFit(t, m, "ansi", size, "wizard")
		for step, k := range []string{"enter", "enter", "enter", "enter", "right", "enter", "enter", "x", "enter", "enter", "enter", "shift+tab", "enter", "esc"} {
			press(m, k)
			mustFit(t, m, "ansi", size, "wizard "+k)
			if (step == 10 || step == 12) && !m.form.reviewing() {
				t.Fatalf("wizard did not reach review at %v", size)
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
