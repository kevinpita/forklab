package tui

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func defaultsField(t *testing.T, f *form, key string) *field {
	t.Helper()
	for i := range f.fields {
		if f.fields[i].key == key {
			return &f.fields[i]
		}
	}
	t.Fatalf("no field %s", key)
	return nil
}

func defaultsModel(t *testing.T) *Model {
	t.Helper()
	m := emptyModel(t)
	m.openForm(labCreateSpec(m, true))
	m.form.loads[Command{"profile", "list"}.String()] = &optionLoad{
		done: true, data: json.RawMessage(`[{"name":"alpha"},{"name":"beta"}]`),
	}
	m.syncForm()
	return m
}

func defaultsProfile(m *Model, name, data string, err error) {
	m.applyFormOptions(formOptionsMsg{form: m.form.id, key: Command{"profile", "show", name}.String(), res: Result{Data: json.RawMessage(data), Err: err}})
}

func TestChainIDProfileDefault(t *testing.T) {
	for _, kind := range []string{"profile", "path", "url", "git", "src"} {
		t.Run(kind, func(t *testing.T) {
			m := defaultsModel(t)
			fill(m.form, map[string]string{"bin-kind": kind})
			m.form.loads = map[string]*optionLoad{Command{"profile", "list"}.String(): m.form.loads[Command{"profile", "list"}.String()]}
			var requests []Command
			m.form.sync(func(c Command) tea.Cmd { requests = append(requests, c); return nil })
			if len(requests) != 1 || requests[0].String() != (Command{"profile", "show", "alpha"}).String() {
				t.Fatalf("profile requests = %v", requests)
			}
			chain := defaultsField(t, m.form, "chain-id")
			if chain.input.Placeholder != "Profile (loading…)" {
				t.Fatalf("pending placeholder = %q", chain.input.Placeholder)
			}
			defaultsProfile(m, "alpha", `{"profile":{"chain_id":"alpha-42","binaries":{"1":{"path":"/bin/a"}}}}`, nil)
			m.form.setFocus(slices.IndexFunc(m.form.fields, func(f field) bool { return f.key == "chain-id" }))
			view := ansi.Strip(strings.Join(m.formView(), "\n"))
			if !strings.Contains(view, "Profile (alpha-42)") || chain.value() != "" || slices.Contains(m.form.command(), "--chain-id") {
				t.Fatalf("display-only chain default failed:\n%s\n%v", view, m.form.command())
			}
			m.form.step = len(m.form.visible())
			view = ansi.Strip(strings.Join(m.formView(), "\n"))
			if !strings.Contains(view, "Profile (alpha-42)") {
				t.Fatalf("review hides profile default:\n%s", view)
			}
			chain.input.SetValue("override-3")
			view = ansi.Strip(strings.Join(m.formView(), "\n"))
			if !strings.Contains(view, "override-3") || !strings.Contains(m.form.command().String(), "--chain-id override-3") {
				t.Fatalf("typed override failed:\n%s", view)
			}
		})
	}
}

func TestProfileSwitchClearsDefaultsWhileLoading(t *testing.T) {
	m := defaultsModel(t)
	fill(m.form, map[string]string{"mode": "fork"})
	m.syncForm()
	defaultsProfile(m, "alpha", `{"profile":{"chain_id":"alpha-42","binaries":{"1":{"path":"/bin/a"}},"snapshots":{"polkachu":"https://a"}}}`, nil)
	chain, snapshot := defaultsField(t, m.form, "chain-id"), defaultsField(t, m.form, "snapshot-source")
	if snapshot.value() != "named:polkachu" {
		t.Fatalf("configured snapshot default = %q", snapshot.value())
	}
	fill(m.form, map[string]string{"profile": "beta"})
	m.syncForm()
	if chain.input.Placeholder != "Profile (loading…)" || len(snapshot.opts) != 0 || snapshot.value() != "" || strings.Contains(m.form.command().String(), "polkachu") {
		t.Fatalf("previous profile leaked while pending: placeholder %q, snapshots %v, command %v", chain.input.Placeholder, snapshot.opts, m.form.command())
	}
	if got := m.form.problem(slices.IndexFunc(m.form.fields, func(f field) bool { return f.key == "snapshot-source" }), m.form.values()); got != "loading options" {
		t.Fatalf("pending snapshot is selectable: %q", got)
	}
	defaultsProfile(m, "beta", "", errors.New("profile unavailable"))
	if chain.input.Placeholder != "Profile (unavailable)" || len(snapshot.opts) != 0 {
		t.Fatalf("failed profile shows stale defaults: %q, %v", chain.input.Placeholder, snapshot.opts)
	}
	defaultsProfile(m, "alpha", `{"profile":{"chain_id":"old-answer","snapshots":{"stale":"https://stale"}}}`, nil)
	if chain.input.Placeholder != "Profile (unavailable)" || len(snapshot.opts) != 0 {
		t.Fatal("late previous-profile response replaced current defaults")
	}
	defaultsProfile(m, "beta", `{"profile":{"chain_id":"beta-7","binaries":{"2":{"path":"/bin/b"}},"snapshots":{"beta-snapshot":"https://b"}}}`, nil)
	if chain.input.Placeholder != "Profile (beta-7)" || snapshot.value() != "named:beta-snapshot" {
		t.Fatalf("new defaults not applied: %q, %q", chain.input.Placeholder, snapshot.value())
	}
}

func TestSnapshotChoicesAndCustomInput(t *testing.T) {
	m := defaultsModel(t)
	fill(m.form, map[string]string{"mode": "fork", "snapshot": "https://custom/archive.tar.zst"})
	m.syncForm()
	defaultsProfile(m, "alpha", `{"profile":{"chain_id":"alpha-42","binaries":{"1":{"path":"/bin/a"}},"snapshots":{"polkachu":"https://templated/{{ .Arch }}","custom":"https://named/custom","z-last":"https://z"}}}`, nil)
	snapshot := defaultsField(t, m.form, "snapshot-source")
	want := []option{{"named:custom", "custom"}, {"named:polkachu", "polkachu"}, {"named:z-last", "z-last"}, {customSnapshot, "Custom URL or file"}}
	if !slices.Equal(snapshot.opts, want) {
		t.Fatalf("snapshot choices = %+v", snapshot.opts)
	}
	if !strings.Contains(m.form.command().String(), "--fork custom") || m.form.values()["snapshot"] != "" {
		t.Fatalf("snapshot named custom collided with custom input: %s", m.form.command())
	}
	m.form.setFocus(slices.IndexFunc(m.form.fields, func(f field) bool { return f.key == "snapshot-source" }))
	view := ansi.Strip(strings.Join(m.formView(), "\n"))
	for _, label := range []string{"custom", "polkachu", "z-last", "Custom URL or file", "↑↓ choose"} {
		if !strings.Contains(view, label) {
			t.Fatalf("snapshot picker hides %q:\n%s", label, view)
		}
	}
	press(m, "down")
	if got := m.form.command().String(); !strings.Contains(got, "--fork polkachu") || strings.Contains(got, "https://") || strings.Contains(got, "named:") || strings.Contains(got, "--chain-id") {
		t.Fatalf("configured snapshot command = %s", got)
	}
	if _, visible := m.form.values()["snapshot"]; visible {
		t.Fatal("custom URL input is visible for a named snapshot")
	}
	press(m, "down", "down", "enter")
	if m.form.fields[m.form.focus].key != "snapshot" || !strings.Contains(m.form.command().String(), "--fork https://custom/archive.tar.zst") {
		t.Fatalf("custom source failed: focus %s, command %s", m.form.fields[m.form.focus].key, m.form.command())
	}
	fill(m.form, map[string]string{"snapshot": "./local.tar.lz4"})
	if !strings.Contains(m.form.command().String(), "--fork ./local.tar.lz4") {
		t.Fatal("local archive not passed to CLI")
	}
	fill(m.form, map[string]string{"profile": "beta"})
	m.syncForm()
	defaultsProfile(m, "beta", `{"profile":{"chain_id":"beta-7","binaries":{"2":{"path":"/bin/b"}}}}`, nil)
	if len(snapshot.opts) != 1 || snapshot.value() != customSnapshot || m.form.values()["snapshot"] != "./local.tar.lz4" {
		t.Fatalf("profile without snapshots cannot use a custom archive: %v, %v", snapshot.opts, m.form.values())
	}
	fill(m.form, map[string]string{"snapshot": ""})
	if m.form.problem(slices.IndexFunc(m.form.fields, func(f field) bool { return f.key == "snapshot" }), m.form.values()) != "required" {
		t.Fatal("custom source accepted an empty URL or file")
	}
}
