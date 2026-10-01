package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type recipeEditor struct {
	Path     string
	Name     string
	Vars     map[string]any
	Steps    []map[string]any
	Cursor   int
	Existing bool
	Dirty    bool
	Busy     bool
	Error    string
	Notice   string
}

func (r *recipeEditor) document() map[string]any {
	return map[string]any{"version": 1, "name": r.Name, "vars": r.Vars, "steps": r.Steps}
}

func (r *recipeEditor) saveCommand() Command {
	data, _ := json.Marshal(r.document())
	c := Command{"runbook", "write", r.Path, "--document", string(data)}
	if r.Existing {
		c = append(c, "--force")
	}
	return c
}

func (m *Model) recipeDone(res Result) {
	r := m.recipe
	if r == nil || len(res.Cmd) < 3 || res.Cmd[0] != "runbook" || res.Cmd[2] != r.Path || !r.Busy {
		return
	}
	if res.Cmd[1] != "show" && res.Cmd[1] != "write" {
		return
	}
	r.Busy = false
	if res.Err != nil {
		r.Error = oneLine(res.Err.Error())
		return
	}
	r.Error = ""
	if res.Cmd[1] == "show" {
		var d struct {
			Name  string           `json:"name"`
			Vars  map[string]any   `json:"vars"`
			Steps []map[string]any `json:"steps"`
		}
		dec := json.NewDecoder(bytes.NewReader(res.Data))
		dec.UseNumber()
		if err := dec.Decode(&d); err != nil {
			r.Error = "Could not read this runbook."
			return
		}
		r.Name, r.Vars, r.Steps = d.Name, d.Vars, d.Steps
	} else {
		r.Existing = true
		r.Dirty = false
		r.Notice = "Saved " + r.Path
	}
}

func recipeIdle(m *Model) bool { return m.recipe != nil && !m.recipe.Busy }

func recipeHasStep(m *Model) bool { return recipeIdle(m) && len(m.recipe.Steps) > 0 }

func (m *Model) openRecipeEditor() tea.Cmd {
	if m.recipe != nil {
		m.openOverlay(overlayRecipe)
		return nil
	}
	return m.openForm(recipeEditorSpec(m))
}

func (r *recipeEditor) move(delta int) {
	r.Cursor = min(max(r.Cursor+delta, 0), max(len(r.Steps)-1, 0))
}

func (r *recipeEditor) remove() {
	r.Steps = slices.Delete(r.Steps, r.Cursor, r.Cursor+1)
	r.move(0)
	r.Dirty, r.Notice = true, ""
}

func (r *recipeEditor) reorder(delta int) {
	next := r.Cursor + delta
	if next < 0 || next >= len(r.Steps) {
		return
	}
	r.Steps[next], r.Steps[r.Cursor] = r.Steps[r.Cursor], r.Steps[next]
	r.Cursor, r.Dirty, r.Notice = next, true, ""
}

func (m *Model) saveRecipe() tea.Cmd {
	r := m.recipe
	if len(r.Steps) == 0 {
		r.Error = "Add at least one step before saving."
		return nil
	}
	r.Busy, r.Error, r.Notice = true, "", ""
	return m.exec(r.saveCommand(), false, 0)
}

func (m *Model) runRecipe() tea.Cmd {
	r := m.recipe
	if r.Dirty || !r.Existing {
		r.Error = "Save your changes before running this runbook."
		return nil
	}
	spec := recipeSpec("run")(m)
	spec.fields[0].def = r.Path
	spec.returnTo = overlayRecipe
	return m.openForm(spec)
}

func stepDescription(s map[string]any) string {
	action := stepAction(s)
	switch action {
	case "tx":
		if tx, ok := s[action].(map[string]any); ok {
			return "from " + fmt.Sprint(tx["from"]) + " | " + argText(tx["args"])
		}
	case "query", "script":
		return argText(s[action])
	case "store":
		if store, ok := s[action].(map[string]any); ok {
			return fmt.Sprint(store["name"]) + " | key " + fmt.Sprint(store["key_hex"])
		}
	case "assert":
		return fmt.Sprint(s[action])
	case "pause", "wait_height":
		return "height " + fmt.Sprint(s[action])
	case "hold":
		return "wait for manual resume"
	case "resume":
		return "resume the chain"
	}
	return ""
}

func (m *Model) recipeView() []string {
	r := m.recipe
	if r == nil {
		return nil
	}
	lines := []string{" " + m.th.Val.Render(r.Path), ""}
	room := max(m.bodyH()-11, 1)
	start := max(r.Cursor-room+1, 0)
	for i := start; i < min(len(r.Steps), start+room); i++ {
		s := r.Steps[i]
		id, _ := s["id"].(string)
		label := fmt.Sprintf("%d  %-12s ", i+1, stepAction(s))
		if id != "" {
			label += id + " | "
		}
		label += stepDescription(s)
		style := m.th.Text
		if i == r.Cursor {
			style = m.th.Accent2
			label = "> " + label
		} else {
			label = "  " + label
		}
		lines = append(lines, " "+style.Render(label))
	}
	if len(r.Steps) == 0 {
		lines = append(lines, " Add a step, choose an action, and fill in its fields.")
	}
	lines = append(lines, "")
	if len(r.Vars) > 0 {
		keys := make([]string, 0, len(r.Vars))
		for key := range r.Vars {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		vars := make([]string, 0, len(keys))
		for _, key := range keys {
			vars = append(vars, key+"="+fmt.Sprint(r.Vars[key]))
		}
		lines = append(lines, " Variables  "+strings.Join(vars, " | "))
	}
	if r.Dirty || !r.Existing {
		lines = append(lines, " Unsaved draft. Close keeps it available with ctrl+e.")
	}
	if r.Busy {
		lines = append(lines, " Working...")
	}
	if r.Error != "" {
		lines = append(lines, " "+m.th.Bad.Render(r.Error))
	}
	if r.Notice != "" {
		lines = append(lines, " "+m.th.Text.Render(r.Notice))
	}
	return m.modal("Runbook builder", r.Name, lines, min(m.w-2, 100))
}
