package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestProposalDetailsUseLoadedPayloadAndWrap(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.panel = panelProposals
	p, _ := m.selectedProposal()
	p.Title = "Detailed title"
	p.Summary = strings.Repeat("full description ", 30)
	p.Metadata = "ipfs://metadata"
	p.MessagePayloads = []json.RawMessage{json.RawMessage(`{"@type":"/custom.Msg","number":9007199254740993}`)}
	m.proposal = &p
	view := proposalMain(m, 42, 20)
	all := ansi.Strip(strings.Join(view.lines, "\n"))
	for _, want := range []string{"Detailed title", "full description", "ipfs://metadata", "9007199254740993"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(all, `  "number": 9007199254740993`) {
		t.Fatal("JSON indentation lost")
	}
	for _, line := range view.lines {
		if ansi.StringWidth(line) > 42 {
			t.Errorf("overwide %q", line)
		}
	}
}

func TestProposalCloneEditSaveRetainsDraftAndNeverSubmits(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.panel = panelProposals
	m.w, m.h = 100, 35
	press(m, "C")
	d := m.proposalDraft
	if d == nil {
		t.Fatal("clone did not open")
	}
	id := d.Action
	res := Result{Cmd: Command{"gov", "draft", "1"}, Data: json.RawMessage(`{"messages":[],"title":"A","summary":"A","metadata":"A","deposit":"10stake"}`)}
	m.proposalEditorDone(actionMsg{id: id + 1, res: res})
	if d.Action != id {
		t.Fatal("accepted stale response")
	}
	m.proposalEditorDone(actionMsg{id: id, res: res})
	if !d.Dirty || !strings.Contains(d.Input.Value(), "10stake") {
		t.Fatal("missing draft")
	}
	before := d.Input.Value()
	m.update(tea.PasteMsg{Content: " "})
	if d.Input.Value() == before {
		t.Fatal("paste not accepted")
	}
	press(m, "esc")
	press(m, "E")
	if m.proposalDraft != d || m.overlay != overlayProposal {
		t.Fatal("draft lost")
	}
	press(m, "ctrl+s")
	fill(m.form, map[string]string{"path": "draft.json"})
	_ = m.formSubmit()
	run := m.running[len(m.running)-1]
	if run.cmd[1] != "write" || strings.Contains(run.cmd.String(), "--force") {
		t.Fatalf("unsafe save %v", run.cmd)
	}
	m.proposalEditorDone(actionMsg{id: d.Action, res: Result{Cmd: run.cmd, Err: errors.New("already exists")}})
	if d.Error == "" || !d.Dirty {
		t.Fatal("failed save lost draft")
	}
	press(m, "esc")
	press(m, "C")
	if m.form == nil {
		t.Fatal("no replacement guard")
	}
	_ = m.formSubmit()
	if m.proposalDraft != d {
		t.Fatal("discarded by default")
	}
	for _, run := range m.running {
		if run.cmd[1] == "submit" {
			t.Fatal("broadcast command")
		}
	}
	for _, size := range [][2]int{{40, 16}, {80, 24}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, line := range strings.Split(m.render(), "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("overflow %v %q", size, line)
			}
		}
	}
}
