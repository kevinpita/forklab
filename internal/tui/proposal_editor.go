package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

type proposalEditor struct {
	Input         textarea.Model
	Path          string
	Dirty         bool
	Action        int
	Error         string
	Notice        string
	SavedDocument string
}

func (m *Model) sizeProposalEditor() {
	if d := m.proposalDraft; d != nil {
		d.Input.SetWidth(max(min(m.w-8, 108), 1))
		d.Input.SetHeight(max(m.bodyH()-9, 1))
	}
}

func (m *Model) cloneProposal() tea.Cmd {
	if m.proposalDraft != nil && m.proposalDraft.Action != 0 {
		return nil
	}
	if m.proposalDraft != nil && m.proposalDraft.Dirty {
		return m.openForm(&formSpec{title: "Replace unsaved proposal draft?", fields: []fieldSpec{{key: "draft", label: "Unsaved draft", kind: fieldSelect, def: "keep", options: []option{{"keep", "Keep current draft"}, {"discard", "Discard and clone"}}}}, build: func(values) Command {
			p, _ := m.selectedProposal()
			return Command{"gov", "draft", strconv.FormatUint(p.ID, 10)}
		}, submit: func(v values) tea.Cmd {
			m.form = nil
			if v["draft"] != "discard" {
				m.overlay = overlayProposal
				return nil
			}
			return m.startProposalClone()
		}})
	}
	return m.startProposalClone()
}

func (m *Model) startProposalClone() tea.Cmd {
	p, ok := m.selectedProposal()
	if !ok {
		return nil
	}
	input := textarea.New()
	input.CharLimit = 0
	input.MaxHeight = 0
	input.MaxWidth = 0
	input.ShowLineNumbers = true
	input.SetValue("")
	m.proposalDraft = &proposalEditor{Input: input, Path: fmt.Sprintf("proposal-%d-draft.json", p.ID)}
	m.openOverlay(overlayProposal)
	m.sizeProposalEditor()
	cmd := m.exec(Command{"gov", "draft", strconv.FormatUint(p.ID, 10)}, false, 0)
	m.proposalDraft.Action = m.actionSeq
	return tea.Batch(cmd, m.proposalDraft.Input.Focus())
}

func (m *Model) proposalEditorInput(msg tea.Msg) tea.Cmd {
	d := m.proposalDraft
	if d == nil || d.Action != 0 {
		return nil
	}
	before := d.Input.Value()
	var cmd tea.Cmd
	d.Input, cmd = d.Input.Update(msg)
	if before != d.Input.Value() {
		d.Dirty = true
		d.Notice = ""
	}
	return cmd
}

func (m *Model) proposalEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.overlay = overlayNone
		return nil
	case "ctrl+s":
		if m.proposalDraft.Action == 0 {
			return m.openForm(proposalSaveSpec(m))
		}
		return nil
	case "ctrl+c":
		return m.quit()
	}
	return m.proposalEditorInput(msg)
}

func (m *Model) proposalEditorDone(msg actionMsg) {
	d := m.proposalDraft
	if d == nil || d.Action == 0 || d.Action != msg.id {
		return
	}
	d.Action = 0
	if msg.res.Err != nil {
		d.Error = msg.res.Err.Error()
		return
	}
	d.Error = ""
	if msg.res.Cmd[1] == "draft" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, msg.res.Data, "", "  "); err != nil {
			d.Error = err.Error()
			return
		}
		d.Input.SetValue(pretty.String())
		d.Dirty = true
		return
	}
	d.Dirty = d.Input.Value() != d.SavedDocument
	d.Notice = "Saved " + d.Path
}

func proposalSaveSpec(m *Model) *formSpec {
	d := m.proposalDraft
	return &formSpec{title: "Save proposal draft", returnTo: overlayProposal, fields: []fieldSpec{{key: "path", label: "JSON file", kind: fieldText, def: d.Path}, {key: "force", label: "Overwrite", kind: fieldToggle}}, build: func(v values) Command {
		c := Command{"gov", "write", v["path"], "--document", d.Input.Value()}
		if v["force"] != "" {
			c = append(c, "--force")
		}
		return c
	}, submit: func(v values) tea.Cmd {
		c := Command{"gov", "write", v["path"], "--document", d.Input.Value()}
		if v["force"] != "" {
			c = append(c, "--force")
		}
		d.Path = v["path"]
		d.SavedDocument = d.Input.Value()
		d.Error = ""
		m.form = nil
		m.overlay = overlayProposal
		cmd := m.exec(c, false, 0)
		d.Action = m.actionSeq
		return cmd
	}}
}

func proposalExportSpec(m *Model) *formSpec {
	p, _ := m.selectedProposal()
	id := strconv.FormatUint(p.ID, 10)
	return &formSpec{title: "Export proposal record", fields: []fieldSpec{{key: "path", label: "JSON file", kind: fieldText, def: "proposal-" + id + ".json"}, {key: "force", label: "Overwrite", kind: fieldToggle}}, build: func(v values) Command {
		c := Command{"gov", "export", id, v["path"]}
		if v["force"] != "" {
			c = append(c, "--force")
		}
		return c
	}}
}

func proposalSubmitFileSpec(*Model) *formSpec {
	return &formSpec{title: "Submit saved proposal", stepped: true, fields: []fieldSpec{{key: "path", label: "JSON file", kind: fieldText}}, build: func(v values) Command { return Command{"gov", "submit", v["path"]} }}
}

func (m *Model) proposalEditorView() []string {
	d := m.proposalDraft
	if d == nil {
		return nil
	}
	lines := []string{" Edit submission JSON. Ctrl+s: save as · Esc: close · E: reopen", ""}
	lines = append(lines, strings.Split(d.Input.View(), "\n")...)
	status := "Draft"
	if d.Dirty {
		status = "Unsaved draft"
	}
	if d.Action != 0 {
		status = "Working..."
	}
	if d.Notice != "" {
		status = d.Notice
	}
	lines = append(lines, "", status)
	if d.Error != "" {
		lines = append(lines, wrapWords(d.Error, max(min(m.w-8, 108), 1))...)
	}
	return m.modal("Proposal draft", d.Path, lines, min(m.w-2, 114))
}
