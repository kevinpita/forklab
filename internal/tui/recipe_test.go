package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestVisualRecipeBuilderCreatesEditsAndReordersSteps(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 140, 45
	_ = m.openForm(recipeEditorSpec(m))
	fill(m.form, map[string]string{"path": "./debug.yaml", "name": "Debug case"})
	_ = m.formSubmit()
	if m.overlay != overlayRecipe || m.recipe == nil {
		t.Fatalf("editor did not open")
	}
	press(m, "a")
	fill(m.form, map[string]string{"action": "tx", "id": "send", "args": "bank send '{{ .accounts.val0.address }}' '{{ .accounts.val1.address }}' 10stake"})
	_ = m.formSubmit()
	press(m, "a")
	fill(m.form, map[string]string{"action": "pause", "height": "40"})
	_ = m.formSubmit()
	if m.overlay != overlayRecipe || len(m.recipe.Steps) != 2 {
		t.Fatalf("draft=%+v", m.recipe)
	}
	press(m, "K")
	if stepAction(m.recipe.Steps[0]) != "pause" {
		t.Fatalf("reorder failed: %v", m.recipe.Steps)
	}
	press(m, "e")
	fill(m.form, map[string]string{"height": "50"})
	_ = m.formSubmit()
	if m.recipe.Steps[0]["pause"] != int64(50) {
		t.Fatalf("edit=%v", m.recipe.Steps[0])
	}
	press(m, "J")
	press(m, "v")
	fill(m.form, map[string]string{"key": "amount", "value": "20stake"})
	_ = m.formSubmit()
	if m.recipe.Vars["amount"] != "20stake" || !m.recipe.Dirty {
		t.Fatalf("vars=%v", m.recipe.Vars)
	}
	c := m.recipe.saveCommand()
	if c[0] != "runbook" || c[1] != "write" || c[2] != "./debug.yaml" {
		t.Fatalf("save=%v", c)
	}
	var doc struct {
		Name  string           `json:"name"`
		Steps []map[string]any `json:"steps"`
	}
	if err := json.Unmarshal([]byte(c[4]), &doc); err != nil {
		t.Fatal(err)
	}
	tx := doc.Steps[0]["tx"].(map[string]any)
	args := tx["args"].([]any)
	if doc.Name != "Debug case" || args[2] != "{{ .accounts.val0.address }}" {
		t.Fatalf("doc=%+v", doc)
	}
	if !strings.Contains(m.footer(), "reorder") {
		t.Fatalf("no controls in builder")
	}
	press(m, "d")
	if len(m.recipe.Steps) != 1 {
		t.Fatalf("remove failed")
	}
}

func TestEditingExistingRecipePreservesVariablesAndArgv(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.recipe = &recipeEditor{Path: "./case.yaml", Existing: true, Busy: true}
	m.recipeDone(Result{Cmd: Command{"runbook", "show", "./case.yaml"}, Data: json.RawMessage(`{"version":1,"name":"old","vars":{"amount":"10"},"steps":[{"id":"check","script":["python3","a b.py","quote's"]}]}`)})
	if m.recipe.Busy || m.recipe.Name != "old" {
		t.Fatalf("load=%+v", m.recipe)
	}
	_ = m.openForm(m.recipeStepSpec(0))
	_ = m.formSubmit()
	step := m.recipe.Steps[0]
	if !reflect.DeepEqual(step["script"], []string{"python3", "a b.py", "quote's"}) || m.recipe.Vars["amount"] != "10" {
		t.Fatalf("round trip=%v vars=%v", step, m.recipe.Vars)
	}
	if c := m.recipe.saveCommand(); c[len(c)-1] != "--force" {
		t.Fatalf("existing file save=%v", c)
	}
}

func TestBuilderCancelReturnsToDraftAndInvalidHeightKeepsForm(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.recipe = &recipeEditor{Path: "case.yaml", Steps: []map[string]any{}}
	_ = m.openForm(m.recipeStepSpec(-1))
	fill(m.form, map[string]string{"action": "pause", "height": "0"})
	_ = m.formSubmit()
	if m.overlay != overlayForm || m.form.valid() || len(m.recipe.Steps) != 0 {
		t.Fatalf("accepted invalid height")
	}
	press(m, "esc")
	if m.overlay != overlayRecipe {
		t.Fatalf("cancel discarded draft")
	}
}

func TestBuilderCatalogHelpAndCloseKeepDraft(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 140, 45
	press(m, "ctrl+e")
	fill(m.form, map[string]string{"path": "draft.yaml"})
	_ = m.formSubmit()
	press(m, "a")
	fill(m.form, map[string]string{"action": "tx", "from": "val1", "args": "bank send recipient 10stake", "expect_code": "0"})
	_ = m.formSubmit()
	if m.overlay != overlayRecipe || m.recipe.Steps[0]["tx"].(map[string]any)["expect_code"] != int64(0) {
		t.Fatalf("success code rejected: overlay=%v", m.overlay)
	}
	draft := m.recipe
	for _, key := range []string{"a", "e", "enter", "d", "J", "K", "j", "k", "v", "s", "r", "o", "esc", "?"} {
		if b, ok := m.match(key); !ok || b.scope != scopeRecipe {
			t.Errorf("builder key %q is absent from its catalog", key)
		}
	}
	view := strings.Join(m.recipeView(), "\n")
	if !strings.Contains(view, "from val1 | bank send recipient 10stake") {
		t.Fatalf("transaction sender or command missing: %s", view)
	}
	press(m, "?")
	if m.overlay != overlayHelp || !strings.Contains(strings.Join(m.helpView(), "\n"), "Move the selected step up") {
		t.Fatal("builder help does not describe its controls")
	}
	press(m, "esc")
	if m.overlay != overlayRecipe || m.recipe != draft {
		t.Fatal("help did not return to the draft")
	}
	press(m, "o", "esc")
	if m.overlay != overlayRecipe || m.recipe != draft {
		t.Fatal("canceling another file discarded the draft")
	}
	press(m, "e")
	fill(m.form, map[string]string{"from": "val0", "args": "discard these changes"})
	press(m, "esc")
	if m.recipe.Steps[0]["tx"].(map[string]any)["from"] != "val1" {
		t.Fatal("canceling the step changed the draft")
	}
	press(m, "esc", "ctrl+e")
	if m.overlay != overlayRecipe || m.recipe != draft || !m.recipe.Dirty {
		t.Fatal("closing and reopening discarded the draft")
	}
}

func TestBuilderSaveBeforeRunAndBusyCommands(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.recipe = &recipeEditor{Path: "draft.yaml", Dirty: true, Steps: []map[string]any{{"hold": true}}}
	m.overlay = overlayRecipe
	if press(m, "r") != nil || len(m.running) != 0 || m.recipe.Error == "" {
		t.Fatal("run accepted an unsaved draft")
	}
	if press(m, "s") == nil || len(m.running) != 1 || !m.recipe.Busy {
		t.Fatal("save did not start the CLI command")
	}
	save := m.running[0].cmd
	if !reflect.DeepEqual(save[:3], Command{"runbook", "write", "draft.yaml"}) {
		t.Fatalf("wrong save command: %v", save)
	}
	for _, key := range []string{"a", "e", "d", "J", "K", "v", "s", "r", "o"} {
		press(m, key)
	}
	if m.overlay != overlayRecipe || m.form != nil || len(m.running) != 1 || len(m.recipe.Steps) != 1 {
		t.Fatal("busy draft accepted an edit or another command")
	}
	press(m, "esc")
	_ = m.applyAction(actionMsg{res: Result{Cmd: save}})
	press(m, "ctrl+e")
	if m.recipe.Busy || m.recipe.Dirty || !m.recipe.Existing {
		t.Fatal("background save did not update the retained draft")
	}
	press(m, "r")
	if m.overlay != overlayForm || m.form.values()["file"] != "draft.yaml" {
		t.Fatal("saved runbook did not open target selection")
	}
	_ = m.formSubmit()
	if !reflect.DeepEqual(m.running[len(m.running)-1].cmd, Command{"runbook", "run", "draft.yaml", "--lab", m.labs[0].Name}) {
		t.Fatal("saved runbook did not run through the CLI")
	}
}

func TestBuilderCommandArgumentsRoundTripExactly(t *testing.T) {
	args := []string{"bank", "send", "", "a b", "quote's", `a"b`, `a\b`, "{{ .vars.amount }}", "{{ .accounts.val1.address }}", " leading and trailing ", "line\nbreak"}
	for _, action := range []string{"tx", "query", "script"} {
		t.Run(action, func(t *testing.T) {
			m := loadedModel(t, buildTheme("ansi", true))
			var payload any = args
			if action == "tx" {
				payload = map[string]any{"from": "val1", "args": args, "expect_code": 0}
			}
			data, err := json.Marshal(map[string]any{"steps": []map[string]any{{action: payload}}})
			if err != nil {
				t.Fatal(err)
			}
			m.recipe = &recipeEditor{Path: "case.yaml", Existing: true, Busy: true}
			m.recipeDone(Result{Cmd: Command{"runbook", "show", "case.yaml"}, Data: data})
			m.overlay = overlayRecipe
			press(m, "e")
			_ = m.formSubmit()
			if m.overlay != overlayRecipe {
				t.Fatalf("unchanged form did not submit: %v", m.form.err)
			}
			got := m.recipe.Steps[0][action]
			if action == "tx" {
				got = got.(map[string]any)["args"]
			}
			if !reflect.DeepEqual(got, args) {
				t.Fatalf("argument round trip changed values: got %q, want %q", got, args)
			}
		})
	}
}

func TestEditingRecipePreservesLargeIntegers(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.recipe = &recipeEditor{Path: "./large.yaml", Existing: true, Busy: true}
	m.recipeDone(Result{Cmd: Command{"runbook", "show", "./large.yaml"}, Data: json.RawMessage(`{"version":1,"vars":{"large":9007199254740993},"steps":[{"pause":1000000}]}`)})
	if m.recipe.Error != "" {
		t.Fatal(m.recipe.Error)
	}
	_ = m.openForm(m.recipeStepSpec(0))
	_ = m.formSubmit()
	if m.overlay != overlayRecipe {
		t.Fatalf("could not edit height: %v", m.form.err)
	}
	command := m.recipe.saveCommand()
	if !strings.Contains(command[4], `"large":9007199254740993`) || !strings.Contains(command[4], `"pause":1000000`) {
		t.Fatalf("integers changed: %s", command[4])
	}
}

func TestRunbookEntryPointsRequireExplicitLab(t *testing.T) {
	for _, builder := range []bool{false, true} {
		m := loadedModel(t, buildTheme("ansi", true))
		m.labs = []labInfo{{Name: "active", Running: true}, {Name: "selected", Running: false}}
		m.cursor[panelLabs] = 1
		path := "case.yaml"
		if builder {
			m.recipe = &recipeEditor{Path: path, Existing: true, Steps: []map[string]any{{"assert": "true"}}}
			m.overlay = overlayRecipe
			press(m, "r")
		} else {
			press(m, "ctrl+t")
		}
		if m.form == nil {
			t.Fatalf("builder=%v: run skipped target selection", builder)
		}
		fill(m.form, map[string]string{"file": path})
		m.cursor[panelLabs] = 0
		_ = m.formSubmit()
		want := Command{"runbook", "run", path, "--lab", "selected"}
		if len(m.running) != 1 || !reflect.DeepEqual(m.running[0].cmd, want) {
			t.Fatalf("builder=%v command=%v want=%v", builder, m.running, want)
		}
	}
}

func TestOpeningAnotherRecipeRequiresExplicitDraftDiscard(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	draft := &recipeEditor{Path: "unsaved.yaml", Dirty: true, Vars: map[string]any{"amount": "10"}, Steps: []map[string]any{{"assert": "true"}}}
	m.recipe = draft
	m.overlay = overlayRecipe
	press(m, "o")
	fill(m.form, map[string]string{"path": "another.yaml"})
	_ = m.formSubmit()
	if m.recipe != draft || len(m.recipe.Steps) != 1 || !m.recipe.Dirty || m.overlay != overlayRecipe {
		t.Fatal("opening another recipe silently discarded unsaved draft")
	}
	press(m, "o")
	fill(m.form, map[string]string{"path": "another.yaml", "draft": "discard"})
	_ = m.formSubmit()
	if m.recipe == draft || m.recipe.Path != "another.yaml" {
		t.Fatal("explicit discard did not allow replacement")
	}
}
