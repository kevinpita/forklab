package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func recipeEditorSpec(m *Model) *formSpec {
	path := ""
	if m.recipe != nil && m.recipe.Dirty {
		path = m.recipe.Path
	}
	fields := []fieldSpec{
		{key: "path", label: "YAML file", kind: fieldText, def: path, placeholder: "./my-test.yaml"},
		{key: "mode", label: "Mode", kind: fieldSelect, def: "new", options: []option{{"new", "Create new"}, {"edit", "Edit existing"}}},
		{key: "name", label: "Name", kind: fieldText, optional: true, show: has("mode", "new")},
	}
	if m.recipe != nil && m.recipe.Dirty {
		fields = append(fields, fieldSpec{key: "draft", label: "Unsaved draft", kind: fieldSelect, def: "keep", options: []option{{"keep", "Keep current draft"}, {"discard", "Discard and open file"}}})
	}
	return &formSpec{title: "Create or edit a runbook", returnTo: m.overlay, fields: fields, build: func(v values) Command {
		if v["mode"] == "edit" {
			return Command{"runbook", "show", v["path"]}
		}
		return Command{"runbook", "write", v["path"], "--document", "DOCUMENT"}
	}, submit: func(v values) tea.Cmd {
		if m.recipe != nil && m.recipe.Dirty && v["draft"] != "discard" {
			m.form = nil
			m.overlay = overlayRecipe
			return nil
		}
		m.recipe = &recipeEditor{Path: v["path"], Name: v["name"], Steps: []map[string]any{}, Existing: v["mode"] == "edit"}
		m.form = nil
		m.overlay = overlayRecipe
		if m.recipe.Existing {
			m.recipe.Busy = true
			return m.exec(Command{"runbook", "show", m.recipe.Path}, false)
		}
		return nil
	}}
}

var recipeActions = []option{{"tx", "Transaction"}, {"query", "Query"}, {"assert", "Assert state (jq)"}, {"wait_height", "Wait for height"}, {"pause", "Pause at exact height"}, {"hold", "Wait for manual resume"}, {"resume", "Resume chain"}, {"store", "Read raw store"}, {"script", "Local script"}}

func stepAction(s map[string]any) string {
	for _, a := range recipeActions {
		if _, ok := s[a.value]; ok {
			return a.value
		}
	}
	return "tx"
}

func argText(v any) string {
	var parts []string
	switch args := v.(type) {
	case []any:
		for _, a := range args {
			parts = append(parts, shellQuote(fmt.Sprint(a)))
		}
	case []string:
		for _, a := range args {
			parts = append(parts, shellQuote(a))
		}
	}
	return strings.Join(parts, " ")
}

func (m *Model) recipeStepSpec(index int) *formSpec {
	r := m.recipe
	initial := values{"action": "tx", "from": "val0"}
	if index >= 0 {
		s := r.Steps[index]
		initial["action"] = stepAction(s)
		for _, key := range []string{"id", "timeout", "at_height"} {
			if v, ok := s[key]; ok {
				initial[key] = fmt.Sprint(v)
			}
		}
		action := initial["action"]
		switch action {
		case "tx":
			if tx, ok := s["tx"].(map[string]any); ok {
				initial["from"] = fmt.Sprint(tx["from"])
				initial["args"] = argText(tx["args"])
				if code, ok := tx["expect_code"]; ok {
					initial["expect_code"] = fmt.Sprint(code)
				}
			}
		case "query", "script":
			initial["args"] = argText(s[action])
		case "assert":
			initial["assert"] = fmt.Sprint(s[action])
		case "wait_height", "pause":
			initial["height"] = fmt.Sprint(s[action])
		case "store":
			if store, ok := s["store"].(map[string]any); ok {
				initial["store"] = fmt.Sprint(store["name"])
				initial["key_hex"] = fmt.Sprint(store["key_hex"])
				if store["prefix"] == true {
					initial["prefix"] = "true"
				}
				if h, ok := store["height"]; ok {
					initial["store_height"] = fmt.Sprint(h)
				}
			}
		}
	}
	is := func(actions ...string) func(values) bool {
		return func(v values) bool { return slices.Contains(actions, v["action"]) }
	}
	fields := []fieldSpec{
		{key: "action", label: "Action", kind: fieldSelect, options: recipeActions, def: initial["action"]},
		{key: "id", label: "Step ID", kind: fieldText, optional: true, def: initial["id"], hint: "for references such as .steps.balances"},
		{key: "from", label: "Signing key", kind: fieldText, def: initial["from"], show: is("tx"), hint: "lab key, for example val0 or val1"},
		{key: "args", label: "Command args", kind: fieldText, def: initial["args"], show: is("tx", "query", "script"), hint: "quoted arguments; templates: '{{ .accounts.val1.address }}'", check: func(s string, _ values) string {
			args, err := splitArgs(s)
			if err != nil {
				return err.Error()
			}
			if len(args) == 0 {
				return "Enter command arguments."
			}
			return ""
		}},
		{key: "expect_code", label: "Expected code", kind: fieldText, optional: true, def: initial["expect_code"], show: is("tx"), hint: "0 means success", check: func(s string, _ values) string {
			if _, err := strconv.ParseUint(s, 10, 32); err != nil {
				return "a whole number from 0 to 4294967295"
			}
			return ""
		}},
		{key: "assert", label: "Expression", kind: fieldText, def: initial["assert"], show: is("assert"), hint: "jq over all step outputs, for example .steps.send.code == 0"},
		{key: "height", label: "Height", kind: fieldNumber, def: initial["height"], show: is("wait_height", "pause")},
		{key: "store", label: "Module store", kind: fieldText, def: initial["store"], show: is("store")},
		{key: "key_hex", label: "Key (hex)", kind: fieldText, def: initial["key_hex"], show: is("store")},
		{key: "prefix", label: "Prefix query", kind: fieldToggle, def: initial["prefix"], show: is("store")},
		{key: "store_height", label: "State height", kind: fieldNumber, optional: true, def: initial["store_height"], show: is("store"), hint: "empty means latest"},
		{key: "at_height", label: "Start at H", kind: fieldNumber, optional: true, def: initial["at_height"], hint: "wait before this step; does not force tx inclusion at H"},
		{key: "timeout", label: "Timeout", kind: fieldText, optional: true, def: initial["timeout"], hint: "for example 2m or 24h for a manual hold"},
	}
	originalArgs := initial["args"]
	displayedArgs := strings.TrimSpace(newFieldInput(m.th, originalArgs).Value())
	buildStep := func(v values) (map[string]any, error) {
		// Single-line inputs sanitize control characters. Preserve unchanged argv.
		if index >= 0 && v["action"] == initial["action"] && v["args"] == displayedArgs {
			v["args"] = originalArgs
		}
		return buildRecipeStep(v)
	}
	return &formSpec{title: "Edit runbook step", fields: fields, returnTo: overlayRecipe, build: func(v values) Command {
		step, err := buildStep(v)
		if err != nil {
			return Command{"runbook", "write", r.Path, "--document", "DOCUMENT"}
		}
		draft := *r
		draft.Steps = slices.Clone(r.Steps)
		if index < 0 {
			draft.Steps = append(draft.Steps, step)
		} else {
			draft.Steps[index] = step
		}
		return draft.saveCommand()
	}, submit: func(v values) tea.Cmd {
		step, err := buildStep(v)
		if err != nil {
			m.form.err = err
			return nil
		}
		if index < 0 {
			r.Steps = append(r.Steps, step)
			r.Cursor = len(r.Steps) - 1
		} else {
			r.Steps[index] = step
		}
		r.Error = ""
		r.Dirty, r.Notice = true, ""
		m.form = nil
		m.overlay = overlayRecipe
		return nil
	}}
}

func buildRecipeStep(v values) (map[string]any, error) {
	s := map[string]any{}
	for _, k := range []string{"id", "timeout"} {
		if v[k] != "" {
			s[k] = v[k]
		}
	}
	number := func(k string) (int64, error) {
		n, err := strconv.ParseInt(v[k], 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s must be a nonnegative integer", k)
		}
		return n, nil
	}
	if v["at_height"] != "" {
		n, err := number("at_height")
		if err != nil {
			return nil, err
		}
		s["at_height"] = n
	}
	action := v["action"]
	switch action {
	case "tx", "query", "script":
		args, err := splitArgs(v["args"])
		if err != nil {
			return nil, err
		}
		if action == "tx" {
			tx := map[string]any{"from": v["from"], "args": args}
			if v["expect_code"] != "" {
				n, err := number("expect_code")
				if err != nil || n > 4294967295 {
					return nil, fmt.Errorf("invalid transaction code")
				}
				tx["expect_code"] = n
			}
			s[action] = tx
		} else {
			s[action] = args
		}
	case "assert":
		s[action] = v["assert"]
	case "wait_height", "pause":
		n, err := number("height")
		if err != nil || n == 0 {
			return nil, fmt.Errorf("height must be positive")
		}
		s[action] = n
	case "resume", "hold":
		s[action] = true
	case "store":
		store := map[string]any{"name": v["store"], "key_hex": v["key_hex"]}
		if v["prefix"] == "true" {
			store["prefix"] = true
		}
		if v["store_height"] != "" {
			n, err := number("store_height")
			if err != nil {
				return nil, err
			}
			store["height"] = n
		}
		s[action] = store
	default:
		return nil, fmt.Errorf("choose an action")
	}
	return s, nil
}

func (m *Model) recipeVariableSpec() *formSpec {
	r := m.recipe
	return &formSpec{title: "Add or update a runbook variable", returnTo: overlayRecipe, fields: []fieldSpec{
		{key: "key", label: "Variable name", kind: fieldText, hint: "reference it with {{ .vars.name }}"},
		{key: "value", label: "Value", kind: fieldText},
	}, build: func(v values) Command {
		draft := *r
		draft.Vars = map[string]any{}
		for k, value := range r.Vars {
			draft.Vars[k] = value
		}
		draft.Vars[v["key"]] = v["value"]
		return draft.saveCommand()
	}, submit: func(v values) tea.Cmd {
		if r.Vars == nil {
			r.Vars = map[string]any{}
		}
		r.Vars[v["key"]] = v["value"]
		r.Dirty, r.Notice = true, ""
		m.form = nil
		m.overlay = overlayRecipe
		return nil
	}}
}
