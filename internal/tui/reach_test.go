package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// unreachable are the CLI commands the TUI does not offer, with why.
var unreachable = map[string]string{
	"tui": "it is the TUI",
}

// labStates are one model per state a lab can be in: running (with a
// running and with an exited node selected), running with an upgrade plan,
// stopped, and none at all.
func labStates(t *testing.T) []*Model {
	t.Helper()
	running := loadedModel(t, buildTheme("ansi", true))
	exited := loadedModel(t, buildTheme("ansi", true))
	exited.cursor[panelNodes] = 1
	planned := loadedModel(t, buildTheme("ansi", true))
	planned.upgrade.Plan = &chainPlan{Name: "v2", Height: 40}
	stopped := loadedModel(t, buildTheme("ansi", true))
	stopped.setStatus(nil)
	feedLabs(t, stopped, `[{"name":"demo","running":false}]`)
	none := emptyModel(t)
	none.wizardOffered = true
	feedLabs(t, none, `[]`)
	builder := loadedModel(t, buildTheme("ansi", true))
	press(builder, "ctrl+e")
	fill(builder.form, map[string]string{"path": "./case.yaml"})
	_ = builder.formSubmit()
	return []*Model{running, exited, planned, stopped, none, builder}
}

// offered is every command the TUI can run or show: each binding's command
// or form in a state where it is enabled, each panel's view, and the
// streams. A binding enabled in no state reaches nothing.
func offered(t *testing.T) []Command {
	t.Helper()
	var out []Command
	for _, m := range labStates(t) {
		for p := range numPanels {
			m.panel = p
			out = append(out, panels[p].view(m))
			for _, b := range catalog {
				if (b.cmd != nil || b.form != nil) && b.enabled(m) {
					out = append(out, b.command(m))
				}
			}
		}
	}
	return append(out, Command{"status", "-w"}, Command{"consensus", "-w"}, logsCmd("0"))
}

// TestEveryCLICommandIsReachable fails when the CLI grows a command the TUI
// does not offer. The fixture is the cobra tree, kept current by the cli
// package's TestCLICommandListFixture.
func TestEveryCLICommandIsReachable(t *testing.T) {
	cmds := offered(t)
	for _, words := range commandPaths {
		path := strings.Join(words, " ")
		if _, skip := unreachable[path]; skip {
			continue
		}
		if !slices.ContainsFunc(cmds, func(c Command) bool { return len(c) >= len(words) && slices.Equal([]string(c[:len(words)]), words) }) {
			t.Errorf("forklab %s is not reachable from the TUI: bind it, add a form, or list it in unreachable with a reason", path)
		}
	}
}

// Every form binding shows in the palette with its command, and in the
// command preview of its panel, as soon as it is enabled.
func TestFormBindingsShowEverywhere(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 200, 60
	for _, b := range catalog {
		if b.form == nil || !b.enabled(m) {
			continue
		}
		want := b.command(m).String()
		found := false
		for _, it := range m.paletteItems() {
			found = found || (it.b.id == b.id && strings.Contains(it.detail, want))
		}
		if !found {
			t.Errorf("palette lacks %q with %s", b.name, want)
		}
		if p, ok := panelOf(b.scope); ok {
			m.panel = p
		}
		found = false
		for _, it := range m.previewItems() {
			found = found || (it.b != nil && it.b.id == b.id && it.cmd.String() == want)
		}
		if !found {
			t.Errorf("preview of %s lacks %s", panels[m.panel].title, want)
		}
		press(m, "c")
		if v := ansi.Strip(m.render()); !strings.Contains(v, strings.ToLower(b.name)) {
			t.Errorf("preview of %s does not render %q", panels[m.panel].title, b.name)
		}
		press(m, "esc")
	}
}

// A form opened from the palette acts like its key: the panel comes into
// view and the form opens there.
func TestPaletteOpensForms(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	press(m, "ctrl+k")
	typeText(m, "send tokens")
	press(m, "enter")
	if m.panel != panelAccounts || m.overlay != overlayForm || m.form.spec.title != "Send tokens" {
		t.Fatalf("panel %v overlay %v", m.panel, m.overlay)
	}
	if v := m.form.values(); v["from"] != "val0" || v["to"] != "val1" {
		t.Fatalf("send starts from %q to %q, want the selected key to the next one", v["from"], v["to"])
	}
}
