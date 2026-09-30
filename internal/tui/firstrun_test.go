package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestStartingLabHidesRawStreamErrors(t *testing.T) {
	m := emptyModel(t)
	m.wizardOffered = true
	feedLabs(t, m, `[{"name":"devnet","running":false}]`)
	m.running = []runningCmd{{cmd: Command{"lab", "up", "devnet"}, at: time.Now()}}
	m.streams[streamStatus].err = &CLIError{Code: "lab_not_running", Message: "lab not running: no node answers: node0: node has no blocks yet"}
	m.Update(tickMsg{})
	v := ansi.Strip(m.render())
	if strings.Contains(v, "no node answers") || !strings.Contains(strings.Split(v, "\n")[0], "Starting the chain…") {
		t.Fatalf("starting lab:\n%s", v)
	}
}

func TestFooterCutsAtWholeHints(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	for _, w := range []int{40, 60, 80, 100, 120} {
		m.Update(windowSize([2]int{w, 30}))
		f := strings.TrimRight(ansi.Strip(m.footer()), " ")
		if ansi.StringWidth(f) > w || !strings.HasSuffix(f, "? help") || strings.Count(f, "? help") != 1 {
			t.Errorf("width %d: footer %q", w, f)
		}
		for _, cut := range []string{"he…", "sto…", "? h…"} {
			if strings.Contains(f, cut) {
				t.Errorf("width %d: footer cuts a hint: %q", w, f)
			}
		}
	}
}

func TestFormFitsASmallTerminal(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.Update(windowSize([2]int{50, 15}))
	press(m, "6", "n")
	typeText(m, "abcdefghijklmnopqrstuvwxyz0123")
	nameFits := func(when string) {
		for _, l := range strings.Split(ansi.Strip(m.render()), "\n") {
			if strings.Contains(l, "Name") && (!strings.Contains(l, "0123") || strings.Contains(l, "…")) {
				t.Fatalf("name field cut %s: %q", when, l)
			}
		}
	}
	nameFits("at 50x15")

	press(m, "esc")
	m.Update(windowSize([2]int{120, 36}))
	press(m, "n")
	typeText(m, "abcdefghijklmnopqrstuvwxyz0123")
	m.Update(windowSize([2]int{50, 15}))
	nameFits("after shrinking from 120x36 to 50x15")
}

func TestPaletteOffersOnlyRealCommands(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	press(m, "ctrl+k")
	typeText(m, "send")
	items := m.paletteItems()
	if len(items) == 0 || items[0].label != "Accounts: Send tokens" {
		t.Fatalf("top item for send = %+v", items)
	}
	for _, it := range items {
		if it.raw != nil {
			t.Errorf("palette offers to run %q", it.detail)
		}
	}
	for _, args := range [][]string{{"lab"}, {"lab", "create", "x", "--profile", "p"}, {"exec", "--", "q"}} {
		if !knownCommand(args) {
			t.Errorf("%q not known", args)
		}
	}
	for _, args := range [][]string{{"send"}, {"lab", "frob"}, {"--json"}} {
		if knownCommand(args) {
			t.Errorf("%q known", args)
		}
	}
}

func TestEscFromAResultReturnsToTheList(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	m.output, m.focus = &Result{Cmd: Command{"version"}}, focusMain
	press(m, "esc", "j")
	if m.output != nil || m.focus != focusList || m.cursor[panelNodes] != 1 {
		t.Fatalf("after esc and j: output %v focus %v cursor %d", m.output != nil, m.focus, m.cursor[panelNodes])
	}
}

func TestWizardStepCountHolds(t *testing.T) {
	m := emptyModel(t)
	feedLabs(t, m, `[]`)
	m.form.fields[0].opts = []option{{"simd", "simd"}}
	m.form.fields[1].opts = []option{{"1", "1"}}
	var seen []string
	for _, k := range []string{"enter", "enter", "enter", "right", "enter", "x", "enter", "enter", "enter"} {
		v := ansi.Strip(m.render())
		i := strings.Index(v, "step ")
		seen = append(seen, v[i:i+11])
		press(m, k)
	}
	for _, s := range seen {
		if !strings.HasSuffix(s, "of 7") {
			t.Fatalf("step counter changed: %q", seen)
		}
	}
}

func TestWizardOpensOncePerSession(t *testing.T) {
	m := emptyModel(t)
	feedLabs(t, m, `[]`)
	if m.overlay != overlayForm {
		t.Fatal("no wizard at startup with zero labs")
	}
	press(m, "esc")
	feedLabs(t, m, `[{"name":"demo","running":false}]`)
	feedLabs(t, m, `[]`)
	if m.overlay != overlayNone {
		t.Fatal("the wizard reopened after the last lab was deleted")
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, "No lab yet. Press n to create one") {
		t.Fatalf("no empty state:\n%s", v)
	}
	if cmd := press(m, "q"); cmd == nil {
		t.Fatal("q did not quit")
	}
}
