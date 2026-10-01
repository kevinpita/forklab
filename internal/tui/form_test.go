package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fill sets fields by key as a user would leave them; a select from a CLI
// source gets the value as its one option.
func fill(f *form, kv map[string]string) {
	for i := range f.fields {
		fl := &f.fields[i]
		s, ok := kv[fl.key]
		if !ok {
			continue
		}
		switch fl.kind {
		case fieldText, fieldNumber:
			fl.input.SetValue(s)
		case fieldToggle:
			fl.on = s == "true"
		case fieldSelect:
			if fl.src != nil {
				fl.opts = []option{{s, s}}
			}
			fl.choice = optionIndex(fl.opts, s)
		}
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		press(m, strings.ReplaceAll(string(r), " ", "space"))
	}
}

func TestNumberFieldEditing(t *testing.T) {
	for _, stepped := range []bool{false, true} {
		name := "flat"
		if stepped {
			name = "stepped"
		}
		t.Run(name, func(t *testing.T) {
			m := loadedModel(t, buildTheme("ansi", true))
			m.openForm(&formSpec{stepped: stepped, fields: []fieldSpec{
				{key: "number", label: "Number", kind: fieldNumber, def: "2", optional: true},
				{key: "text", label: "Text", kind: fieldText},
			}, build: func(v values) Command { return Command{"test", v["number"], v["text"]} }})
			input := &m.form.fields[0].input
			assert := func(want string) {
				t.Helper()
				if got := input.Value(); got != want {
					t.Errorf("number = %q, want %q", got, want)
				}
				if m.form.focus != 0 || m.form.reviewing() {
					t.Fatal("editing moved away from the number")
				}
			}
			press(m, "x", "space", ".", "-", "é")
			assert("2")
			input.SetValue("2")
			input.CursorEnd()
			m.Update(tea.PasteMsg{Content: "9x"})
			assert("2")
			m.Update(tea.PasteMsg{Content: " 3\n"})
			assert("2")
			m.Update(tea.KeyPressMsg{Code: '4', Text: "4x"})
			assert("2")
			input.SetValue("2")
			input.CursorEnd()
			m.Update(tea.PasteMsg{Content: "34"})
			assert("234")
			press(m, "left", "right", "left", "backspace")
			m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
			assert("2")
			press(m, "up")
			assert("3")
			press(m, "down", "down", "down")
			assert("1")
			press(m, "backspace")
			assert("")
			if m.form.problem(0, m.form.values()) != "" {
				t.Fatal("optional empty number is invalid")
			}
			press(m, "down")
			assert("1")
			input.SetValue("")
			press(m, "up")
			assert("1")
			input.SetValue("0")
			press(m, "up")
			assert("1")
			input.SetValue("0")
			press(m, "down")
			assert("1")
			input.SetValue("18446744073709551616")
			press(m, "up")
			assert("18446744073709551617")
			press(m, "down")
			assert("18446744073709551616")
			if !strings.Contains(ansi.Strip(strings.Join(m.formTail(100), "\n")), "↑↓ adjust") {
				t.Fatal("number modal does not explain the arrow keys")
			}
			found := false
			for _, b := range m.footerBindings() {
				found = found || b.footerKey()+" "+b.hint == "↑↓ adjust"
			}
			if !found {
				t.Fatal("number footer does not explain the arrow keys")
			}
			press(m, "tab")
			typeText(m, "x.-")
			m.Update(tea.PasteMsg{Content: " text"})
			if got := m.form.fields[1].input.Value(); got != "x.- text" {
				t.Fatalf("text input = %q", got)
			}
			m.form.setFocus(0)
			if stepped {
				m.form.step = len(m.form.visible())
				press(m, "up", "down")
				if input.Value() != "18446744073709551616" || !m.form.reviewing() {
					t.Fatal("arrows changed a number during review")
				}
			}
			m.form.running = Command{"test"}
			press(m, "up", "down", "5")
			m.Update(tea.PasteMsg{Content: "6"})
			if input.Value() != "18446744073709551616" {
				t.Fatal("running form accepted input")
			}
		})
	}
}

func TestFormArgv(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	labForm := func(m *Model) *formSpec { return labCreateSpec(m, false) }
	wizard := func(m *Model) *formSpec { return labCreateSpec(m, true) }
	tests := []struct {
		name string
		spec func(*Model) *formSpec
		set  map[string]string
		want string
	}{
		{"lab create, nothing filled", labForm, nil, "forklab lab create NAME --profile PROFILE --version VERSION --validators 2 --json"},
		{
			"lab create fresh", labForm,
			map[string]string{"name": "demo2", "profile": "lsimd", "version": "0.53.8", "validators": "3", "snapshot": "ignored"},
			"forklab lab create demo2 --profile lsimd --version 0.53.8 --validators 3 --json",
		},
		{
			"lab create fork", labForm,
			map[string]string{"name": "f", "profile": "xrplevm", "version": "11.1.1", "mode": "fork", "snapshot": "/tmp/s.tar.lz4", "chain-id": "xrplevm_1449999-1"},
			"forklab lab create f --profile xrplevm --version 11.1.1 --validators 2 --fork /tmp/s.tar.lz4 --chain-id xrplevm_1449999-1 --json",
		},
		{
			"first-lab wizard", wizard,
			map[string]string{"profile": "lsimd", "version": "0.53.8"},
			"forklab lab create devnet --profile lsimd --version 0.53.8 --validators 2 --json",
		},
		{
			"profile create", profileCreateSpec,
			map[string]string{"name": "mysimd", "bin-version": "0.53.8", "bin-location": "/x/simd", "block-time": "500ms"},
			"forklab profile create mysimd --from simd --block-time 500ms --binary 0.53.8=path:/x/simd --json",
		},
		{
			"profile edit, changed fields only", profileEditSpec,
			map[string]string{"voting-period": "30s", "expedited-voting-period": "", "bin-version": "0.54.0", "bin-kind": "git", "bin-location": "https://g/x", "bin-ref": "v0.54.0", "bin-build": "make build", "bin-out": "build/simd"},
			"forklab profile edit lsimd --binary 0.54.0=git:https://g/x --binary-ref 0.54.0=v0.54.0 --binary-build '0.54.0=make build' --binary-out 0.54.0=build/simd --voting-period 30s --expedited-voting-period '' --json",
		},
		{
			"upgrade schedule in blocks", upgradeScheduleSpec,
			map[string]string{"version": "11.2.0", "in": "30", "no-auto-swap": "true"},
			"forklab upgrade schedule 11.2.0 --in 30 --no-auto-swap --json",
		},
		{
			"upgrade schedule at a height", upgradeScheduleSpec,
			map[string]string{"version": "11.2.0", "at": "height", "in": "30", "height": "900", "name": "v11.2.0", "expedited": "true"},
			"forklab upgrade schedule 11.2.0 --height 900 --name v11.2.0 --expedited --json",
		},
		{
			"send to a key", sendSpec,
			map[string]string{"from": "val0", "to": "test0", "amount": "1000", "denom": "stake"},
			"forklab account send val0 test0 1000stake --json",
		},
		{
			"send to an address", sendSpec,
			map[string]string{"from": "gov", "to": otherAddress, "address": "cosmos1xyz", "amount": "5", "denom": "stake"},
			"forklab account send gov cosmos1xyz 5stake --json",
		},
		{
			"text proposal", govSubmitSpec,
			map[string]string{"title": "hello world"},
			"forklab gov submit --template text --title 'hello world' --auto-vote --json",
		},
		{
			"params proposal", govSubmitSpec,
			map[string]string{"template": "params", "module": "staking", "set": "max_validators=10 bond_denom=stake", "auto-vote": "false", "plan": "hidden"},
			"forklab gov submit --template params --module staking --set max_validators=10 --set bond_denom=stake --json",
		},
		{
			"upgrade proposal", govSubmitSpec,
			map[string]string{"template": "upgrade", "plan": "v2", "height": "100", "info": "x", "expedited": "true"},
			"forklab gov submit --template upgrade --name v2 --height 100 --info x --expedited --auto-vote --json",
		},
		{"vote", govVoteSpec, map[string]string{"option": "veto", "from": "val0,val1"}, "forklab gov vote 1 veto --from val0,val1 --json"},
		{"restart on a version", nodeRestartSpec, map[string]string{"node": "1", "version": "0.54.0"}, "forklab node restart 1 --binary 0.54.0 --json"},
		{"binary fetch", binarySpec("fetch"), map[string]string{"version": "0.53.8"}, "forklab binary fetch 0.53.8 --profile lsimd --json"},
		{"binary build", binarySpec("build"), map[string]string{"profile": "simd", "version": "dev", "no-verify": "true"}, "forklab binary build dev --profile simd --no-verify --json"},
		{"exec", execSpec, map[string]string{"args": `q bank balances "a b"`}, "forklab exec --json -- q bank balances 'a b'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newForm(m.th, 1, tt.spec(m))
			fill(f, tt.set)
			if got := f.command().String(); got != tt.want {
				t.Errorf("argv\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func openUpgradeForm(t *testing.T, run Runner) (*Model, tea.Cmd) {
	t.Helper()
	m := loadedModel(t, buildTheme("ansi", true))
	m.run = run
	m.w, m.h = 120, 40
	press(m, "4")
	cmd := press(m, "u")
	if m.overlay != overlayForm || m.form.spec.title != "Schedule upgrade" {
		t.Fatalf("u in Upgrades: overlay %v", m.overlay)
	}
	return m, cmd
}

func TestFormTabSkipsHiddenFields(t *testing.T) {
	m, _ := openUpgradeForm(t, Runner{Exe: "/nonexistent/forklab"})
	order := func() string {
		var keys []string
		for range len(m.form.visible()) {
			keys = append(keys, m.form.fields[m.form.focus].key)
			press(m, "tab")
		}
		return strings.Join(keys, " ")
	}
	if got := order(); got != "version at in name no-auto-swap expedited" {
		t.Fatalf("tab order = %q", got)
	}
	press(m, "shift+tab")
	if k := m.form.fields[m.form.focus].key; k != "expedited" {
		t.Fatalf("shift+tab from the first field went to %q, want a wrap to expedited", k)
	}
	press(m, "tab", "tab", "right")
	if got := order(); got != "at height name no-auto-swap expedited version" {
		t.Fatalf("after choosing a height, tab order = %q", got)
	}
	press(m, "tab", "tab", "tab", "space")
	if !strings.Contains(m.form.command().String(), "--no-auto-swap") {
		t.Fatalf("space on the manual swap toggle: %s", m.form.command())
	}
}

func TestFormValidatesBeforeRunning(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	press(m, "6", "n")
	if m.form == nil || m.form.spec.title != "New lab" {
		t.Fatal("n in Labs did not open the lab form")
	}
	if cmd := press(m, "enter"); cmd != nil || len(m.running) != 0 {
		t.Fatal("an empty form ran")
	}
	if k := m.form.fields[m.form.focus].key; k != "name" {
		t.Fatalf("focus on %q, want the first invalid field", k)
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, "✗ required") {
		t.Fatalf("no inline error:\n%s", v)
	}
	typeText(m, "demo")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "a lab named demo exists") {
		t.Fatalf("taken name not flagged:\n%s", v)
	}
	press(m, "backspace", "backspace", "backspace", "backspace")
	typeText(m, "my lab")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "letters, digits, dots, dashes, underscores") {
		t.Fatalf("name with a space not flagged:\n%s", v)
	}
	press(m, "tab", "tab", "tab", "tab")
	press(m, "backspace")
	typeText(m, "0")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "a whole number above 0") {
		t.Fatalf("0 validators not flagged:\n%s", v)
	}
}

func TestFormPreviewFollowsTyping(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 140, 40
	press(m, "6", "n")
	typeText(m, "ab")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "$ forklab lab create ab --profile PROFILE") {
		t.Fatalf("preview does not show the typed name:\n%s", v)
	}
	typeText(m, "c")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "$ forklab lab create abc --profile PROFILE") {
		t.Fatalf("preview did not follow the next key:\n%s", v)
	}
	if press(m, "ctrl+c"); m.overlay != overlayNone || m.form != nil {
		t.Fatal("ctrl+c did not close the form")
	}
}

// drive runs cmd and every command its messages lead to, feeding CLI
// answers back through Update, until done holds. Ticks are dropped so
// nothing reschedules forever.
func drive(t *testing.T, m *Model, cmd tea.Cmd, done func() bool) {
	t.Helper()
	msgs := make(chan tea.Msg, 64)
	start := func(c tea.Cmd) {
		if c != nil {
			go func() { msgs <- c() }()
		}
	}
	start(cmd)
	deadline := time.After(20 * time.Second)
	for !done() {
		select {
		case msg := <-msgs:
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					start(c)
				}
			case formOptionsMsg, actionMsg, resultMsg:
				_, next := m.Update(msg)
				start(next)
			}
		case <-deadline:
			t.Fatalf("timed out; view:\n%s", ansi.Strip(m.render()))
		}
	}
}

func TestFormOptionsComeFromTheCLI(t *testing.T) {
	m, cmd := openUpgradeForm(t, fixtureRunner())
	drive(t, m, cmd, func() bool { return len(m.form.fields[0].opts) > 0 })
	if o := m.form.fields[0].opts; len(o) != 1 || o[0].value != "0.53.8" || !strings.Contains(o[0].label, "(running)") {
		t.Fatalf("version options from lab list = %+v", o)
	}

	press(m, "esc", "6")
	drive(t, m, press(m, "n"), func() bool { return m.form.values()["version"] != "" })
	if got := m.form.command().String(); got != "forklab lab create NAME --profile lsimd --version 0.53.8 --validators 2 --json" {
		t.Fatalf("profile and version options did not load in turn: %s", got)
	}
}

func TestBinaryFetchDefaultsToTheNewestVersion(t *testing.T) {
	m, _ := openUpgradeForm(t, fixtureRunner())
	press(m, "esc", "8")
	drive(t, m, press(m, "f"), func() bool { return m.form.values()["version"] != "" })
	if got := m.form.command().String(); got != "forklab binary fetch 0.54.0 --profile lsimd --json" {
		t.Fatalf("fetch form builds %s", got)
	}
}

func TestUpgradeVersionsMarkWhatTheNodesRun(t *testing.T) {
	lab := `[{"name":"xrp","running":true,"version":"11.1.1",
		"profile":{"name":"xrp","binaries":{"11.1.1":{"path":"/old"},"11.2.0":{"path":"/new"}}},
		"nodes":[{"index":0,"name":"node0","version":"11.2.0"},{"index":1,"name":"node1","version":"11.2.0"}]}]`
	opts, err := labVersions.parse(json.RawMessage(lab), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 || opts[0].value != "11.1.1" || strings.Contains(opts[0].label, "running") || opts[1].value != "11.2.0" || !strings.HasSuffix(opts[1].label, "(running)") {
		t.Fatalf("options after an upgrade to 11.2.0 = %+v, want 11.2.0 last and marked running", opts)
	}
}

func emptyModel(t *testing.T) *Model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, fixtureRunner(), buildTheme("ansi", true))
	m.w, m.h = 120, 40
	return m
}

func feedLabs(t *testing.T, m *Model, data string) tea.Cmd {
	t.Helper()
	m.loads[loadLabs].seq++
	_, cmd := m.Update(resultMsg{kind: loadLabs, seq: m.loads[loadLabs].seq, res: Result{Cmd: Command{"lab", "list"}, Data: []byte(data)}})
	return cmd
}

func TestFirstLabWizard(t *testing.T) {
	m := emptyModel(t)
	feedLabs(t, m, `[]`)
	if m.overlay != overlayForm || m.form.spec.title != "Create your first lab" {
		t.Fatalf("zero labs: overlay %v, want the first-lab wizard", m.overlay)
	}
	press(m, "esc")
	feedLabs(t, m, `[]`)
	if m.overlay != overlayNone {
		t.Fatal("the wizard reopened after it was dismissed")
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, "No lab yet. Press n to create one") {
		t.Fatalf("no friendly empty state:\n%s", v)
	}
	cmd := press(m, "n")
	if m.form == nil || !m.form.spec.stepped {
		t.Fatal("n did not reopen the wizard")
	}
	drive(t, m, cmd, func() bool { return m.form.values()["version"] != "" })
	for _, step := range []string{"Profile", "Binary source", "Version", "Validators", "Genesis", "Chain ID", "Name"} {
		if v := ansi.Strip(m.render()); !strings.Contains(v, step) {
			t.Fatalf("step %s not shown:\n%s", step, v)
		}
		press(m, "enter")
	}
	v := ansi.Strip(m.render())
	for _, want := range []string{"Review", "devnet", "fresh genesis", "$ forklab lab create devnet --profile lsimd"} {
		if !strings.Contains(v, want) {
			t.Fatalf("review lacks %q:\n%s", want, v)
		}
	}
	if got := m.form.command().String(); got != "forklab lab create devnet --profile lsimd --version 0.53.8 --validators 2 --json" {
		t.Fatalf("wizard builds %s", got)
	}
	drive(t, m, press(m, "enter"), func() bool { return m.overlay == overlayConfirm })
	if m.pending == nil || m.pending.cmd.String() != "forklab lab up devnet --json" || !strings.Contains(m.pending.prompt, "Bring it up now?") {
		t.Fatalf("after create: pending %+v", m.pending)
	}
}

func TestFormErrorKeepsTheInput(t *testing.T) {
	m := emptyModel(t)
	cmd := feedLabs(t, m, `[]`)
	drive(t, m, cmd, func() bool { return m.form.values()["version"] != "" })
	press(m, "enter", "enter", "enter", "enter", "enter", "enter")
	for range len("devnet") {
		press(m, "backspace")
	}
	typeText(m, "taken")
	press(m, "enter")
	drive(t, m, press(m, "enter"), func() bool { return m.form.err != nil })
	if m.overlay != overlayForm || m.form.values()["name"] != "taken" {
		t.Fatal("a failed run lost the form")
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, "✗ lab taken already exists") {
		t.Fatalf("form does not show the error:\n%s", v)
	}
}

func TestLongRunCanGoToTheBackground(t *testing.T) {
	m := emptyModel(t)
	cmd := feedLabs(t, m, `[]`)
	drive(t, m, cmd, func() bool { return m.form.values()["version"] != "" })
	press(m, "enter", "enter", "enter", "enter", "enter", "enter", "enter")
	run := press(m, "enter")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "Esc keeps it running in the background") {
		t.Fatalf("running form shows no progress:\n%s", v)
	}
	press(m, "esc")
	if m.overlay != overlayNone || len(m.running) != 1 {
		t.Fatalf("esc: overlay %v, %d running", m.overlay, len(m.running))
	}
	if line := ansi.Strip(m.statusLine()); !strings.Contains(line, "$ forklab lab create devnet") {
		t.Fatalf("status line = %q", line)
	}
	drive(t, m, run, func() bool { return len(m.running) == 0 })
	if m.overlay != overlayNone || m.form != nil {
		t.Fatal("a backgrounded form came back")
	}
	if line := ansi.Strip(m.statusLine()); !strings.Contains(line, "✓ ok") {
		t.Fatalf("status line = %q", line)
	}
}

func TestEmptyStatesFollowTheLab(t *testing.T) {
	noLab := `{"ok":false,"error":{"code":"usage","message":"--lab is required: no lab exists; create one with forklab lab create"}}`
	m := emptyModel(t)
	m.wizardOffered = true
	feedLabs(t, m, `[]`)
	m.loads[loadNodes].seq++
	data, _, err := decodeEnvelope([]byte(noLab))
	m.Update(resultMsg{kind: loadNodes, seq: m.loads[loadNodes].seq, res: Result{Cmd: Command{"node", "list"}, Data: data, Err: err}})
	v := ansi.Strip(m.render())
	if strings.Contains(v, "--lab is required") || !strings.Contains(v, "No lab yet. Press n to create one") {
		t.Fatalf("no lab:\n%s", v)
	}

	feedLabs(t, m, `[{"name":"demo","running":false}]`)
	v = ansi.Strip(m.render())
	if !strings.Contains(v, "Lab demo is stopped. Press u to start it") {
		t.Fatalf("stopped lab:\n%s", v)
	}
	if press(m, "u"); len(m.running) != 1 || m.running[0].cmd.String() != "forklab lab up demo --json" {
		t.Fatalf("u ran %+v", m.running)
	}

	m = loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	m.nodes = nil
	m.loads[loadNodes].seq++
	boom := Result{Cmd: Command{"node", "list"}, Err: &CLIError{Code: "error", Message: "supervisor socket: permission denied"}}
	m.Update(resultMsg{kind: loadNodes, seq: m.loads[loadNodes].seq, res: boom})
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	body := strings.Join(lines[:len(lines)-2], "\n")
	if strings.Contains(body, "permission denied") || !strings.Contains(body, "the error is in the status line") {
		t.Fatalf("unexpected error in the panel body:\n%s", body)
	}
	if !strings.Contains(lines[len(lines)-2], "permission denied") {
		t.Fatalf("status line = %q", lines[len(lines)-2])
	}

	// A lab that just stopped: the lab list still says running.
	m.last = nil
	m.loads[loadUpgrade].seq++
	stopped := Result{Cmd: Command{"upgrade", "status"}, Err: &CLIError{Code: "usage", Message: "--lab is required when no lab is running and several exist: a, b"}}
	m.Update(resultMsg{kind: loadUpgrade, seq: m.loads[loadUpgrade].seq, res: stopped})
	if m.last != nil {
		t.Fatalf("status line took a lab-resolution error: %v", m.last.Err)
	}
}

func TestWrapWordsKeepsFlagsWhole(t *testing.T) {
	got := wrapWords("$ forklab lab create devnet --validators 2 --json", 20)
	want := []string{"$ forklab lab create", "devnet --validators", "2 --json"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapWords = %q, want %q", got, want)
	}
	if got := wrapWords("a /very/long/path/to/simd b", 8); strings.Join(got, "|") != "a|/very/lo|ng/path/|to/simd|b" {
		t.Errorf("long word = %q", got)
	}
}

// The live command wraps but never loses a character, however long.
func TestFormCommandIsNeverCut(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 80, 30
	press(m, "7", "n")
	fill(m.form, map[string]string{"name": "p", "bin-version": "1.0.0", "bin-location": "/tmp/" + strings.Repeat("very-long-dir/", 12) + "simd"})
	var got []string
	inner := m.formW() - 2
	for _, l := range m.formTail(inner)[1:] {
		if w := ansi.StringWidth(l); w > inner {
			t.Errorf("tail line is %d cells, the form %d: %q", w, inner, ansi.Strip(l))
		}
		if strings.Contains(ansi.Strip(l), "Enter run") {
			break
		}
		got = append(got, strings.TrimSpace(ansi.Strip(l)))
	}
	nospace := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	if want := "$ " + m.form.command().String(); nospace(strings.Join(got, "")) != nospace(want) {
		t.Fatalf("tail\n%s\nwant %s", strings.Join(got, "\n"), want)
	}
}

func TestFooterHidesShadowedKeys(t *testing.T) {
	m := emptyModel(t)
	m.wizardOffered = true
	feedLabs(t, m, `[]`)
	hints := func() string {
		var out []string
		for _, b := range m.footerBindings() {
			out = append(out, b.footerKey()+" "+b.hint)
		}
		return strings.Join(out, ",")
	}
	if h := hints(); !strings.Contains(h, "n new lab") {
		t.Fatalf("Nodes footer without labs = %s", h)
	}
	press(m, "7")
	if h := hints(); strings.Contains(h, "n new lab") || !strings.Contains(h, "n new") {
		t.Fatalf("Profiles footer = %s; n makes a profile there", h)
	}
}

func TestStatusLineKeepsTheOutcomeOfALongCommand(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 80, 24
	m.last = &Result{Cmd: Command{"profile", "create", "p", "--binary", "1=path:/" + strings.Repeat("x", 120)}, Took: time.Second}
	if line := ansi.Strip(m.statusLine()); !strings.Contains(line, "✓ ok") || ansi.StringWidth(line) > 80 {
		t.Fatalf("status line = %q", line)
	}
}

func TestAnotherLabRestartsTheChainStreams(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	before := m.streams[streamStatus].session
	feedLabs(t, m, `[{"name":"demo","running":true}]`)
	if m.streams[streamStatus].session != before || m.status == nil {
		t.Fatal("the same lab restarted the streams")
	}
	feedLabs(t, m, `[{"name":"demo","running":false},{"name":"xrpl","running":true}]`)
	if m.streams[streamStatus].session == before || m.streams[streamConsensus].session == before || m.status != nil {
		t.Fatalf("status still shows %+v after xrpl started", m.status)
	}
}
