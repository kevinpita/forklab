package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// envelopeData unwraps a captured envelope the way Runner.Run does.
func envelopeData(t *testing.T, name string) Result {
	t.Helper()
	data, ok, err := decodeEnvelope(testdata(t, name))
	if !ok {
		t.Fatalf("%s holds no envelope", name)
	}
	return Result{Data: data, Err: err}
}

// feed sends a captured load result through Update as if its command had
// just returned.
func feed(t *testing.T, m *Model, k loadKind, name string) {
	t.Helper()
	m.loads[k].seq++
	m.Update(resultMsg{kind: k, seq: m.loads[k].seq, res: envelopeData(t, name)})
	if err := m.loads[k].err; err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func feedStream(t *testing.T, m *Model, k streamKind, name string) {
	t.Helper()
	m.streams[k].session = 1
	for _, line := range bytes.Split(bytes.TrimSpace(testdata(t, name)), []byte("\n")) {
		m.Update(streamEvent{kind: k, session: 1, line: line})
	}
}

// loadedModel is a model fed every captured CLI answer. Its runner points
// nowhere, so the log stream it starts fails fast and harmlessly.
func loadedModel(t *testing.T, th Theme) *Model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, Runner{Exe: "/nonexistent/forklab"}, th)
	feedStream(t, m, streamStatus, "status_w.ndjson")
	feedStream(t, m, streamConsensus, "consensus_w.ndjson")
	feed(t, m, loadNodes, "node_list_stopped.json")
	feed(t, m, loadProposals, "gov_list.json")
	feed(t, m, loadProposal, "gov_show.json")
	feed(t, m, loadUpgrade, "upgrade_status.json")
	feed(t, m, loadAccounts, "account_list.json")
	feed(t, m, loadLabs, "lab_list.json")
	feed(t, m, loadProfiles, "profile_list.json")
	feed(t, m, loadProfile, "profile_show.json")
	feed(t, m, loadBinaries, "binary_list.json")
	for _, line := range bytes.Split(testdata(t, "node_logs.ndjson"), []byte("\n")) {
		var l logLine
		if decodeLine(line, &l) == nil {
			m.logs.add(l.Line, 10)
		}
	}
	return m
}

func TestDecodeCapturedCLIOutput(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	if s := m.status; s == nil || s.Lab != "demo" || s.Height != 13 || len(s.Validators) != 2 || s.Validators[0].PowerPercent != 50 {
		t.Errorf("status = %+v", m.status)
	}
	c := m.consensus
	if c == nil || len(c.Validators) != 2 || c.LastCommit.Fraction != 1 || c.Validators[0].LastCommit.Kind != "block" || c.Nodes[0].Peers[0] != "node1" {
		t.Errorf("consensus = %+v", c)
	}
	if len(m.nodes) != 2 || m.nodes[1].State != "exited" || m.nodes[1].ExitCode == nil || m.nodes[0].PID == 0 {
		t.Errorf("nodes = %+v", m.nodes)
	}
	if len(m.proposals) != 1 || m.proposals[0].VotingEndTime == nil || m.proposal.Tally == nil || m.proposal.Tally.Yes != "2000000000000000000000" {
		t.Errorf("proposals = %+v, shown %+v", m.proposals, m.proposal)
	}
	if u := m.upgrade; u == nil || u.Plan != nil || len(u.Nodes) != 2 || u.Nodes[0].Version != "0.53.8" {
		t.Errorf("upgrade = %+v", m.upgrade)
	}
	if len(m.accounts) != 8 || m.accounts[2].Name != "gov" || m.accounts[2].Balances[0].Denom != "stake" {
		t.Errorf("accounts = %+v", m.accounts)
	}
	if len(m.labs) != 1 || !m.labs[0].Running || m.labs[0].Profile.Name != "lsimd" || m.labs[0].Nodes[1].Ports[1].Port != 26757 {
		t.Errorf("labs = %+v", m.labs)
	}
	if len(m.profiles) != 3 || m.profile == nil || !bytes.Contains(m.profile.Profile, []byte(`"voting_period":"20s"`)) {
		t.Errorf("profiles = %+v, shown %+v", m.profiles, m.profile)
	}
	if len(m.binaries) != 1 || m.binaries[0].Kind != "path" || m.binaries[0].Size == 0 {
		t.Errorf("binaries = %+v", m.binaries)
	}
	if len(m.logs.lines) != 5 {
		t.Errorf("logs = %d lines, want 5", len(m.logs.lines))
	}
}

func TestUpgradesPanelShowsACompletedUpgrade(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.streamLab = "up14"
	m.status.Lab = "up14"
	feed(t, m, loadUpgrade, "upgrade_status_completed.json")
	v := ansi.Strip(strings.Join(upgradeMain(m, 100, 30).lines, "\n"))
	if !strings.HasPrefix(v, "LAST  v11.2.0  to 11.2.0  completed at 42\n") || strings.Contains(v, "no upgrade plan") || strings.Contains(v, "SWAP") || strings.Contains(v, "swapped") {
		t.Errorf("Upgrades after a completed upgrade:\n%s", v)
	}
	if got := upgradeSummary(m); got != "done v11.2.0" {
		t.Errorf("Upgrades title = %q, want done v11.2.0", got)
	}
}

func TestErrorEnvelopeCarriesCode(t *testing.T) {
	r := envelopeData(t, "err_usage.json")
	var ce *CLIError
	if r.Err == nil || !strings.Contains(r.Err.Error(), "unknown command") {
		t.Fatalf("err = %v", r.Err)
	}
	if ce, _ = r.Err.(*CLIError); ce == nil || ce.Code != "usage" {
		t.Fatalf("code = %+v, want usage", ce)
	}
	var s labStatus
	err := decodeLine([]byte(`{"ok":false,"error":{"code":"lab_not_running","message":"no lab is running"}}`), &s)
	if !isLabNotRunning(err) {
		t.Fatalf("stream start failure = %v, want lab_not_running", err)
	}
}

func TestStaleResultsAreDropped(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.loads[loadNodes].seq = 5
	m.Update(resultMsg{kind: loadNodes, seq: 4, res: Result{Data: []byte(`[]`)}})
	if len(m.nodes) != 2 {
		t.Fatalf("stale node list replaced the current one: %+v", m.nodes)
	}
	m.Update(resultMsg{kind: loadNodes, seq: 5, res: Result{Data: []byte(`[]`)}})
	if len(m.nodes) != 0 {
		t.Fatalf("current node list ignored: %+v", m.nodes)
	}

	m.streams[streamStatus].session = 9
	before := m.status.Height
	m.Update(streamEvent{kind: streamStatus, session: 8, line: []byte(`{"lab":"old","height":1}`)})
	if m.status.Height != before {
		t.Fatal("event from a replaced stream was applied")
	}
}

func TestLabNotRunningClearsHeader(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 30
	m.Update(streamEvent{
		kind: streamStatus, session: m.streams[streamStatus].session,
		line: []byte(`{"ok":false,"error":{"code":"lab_not_running","message":"no lab running"}}`),
	})
	if m.status != nil || m.chainUp() {
		t.Fatal("status kept after lab_not_running")
	}
	if h := ansi.Strip(m.header()); strings.Contains(h, "no lab running") || !strings.Contains(h, "Starting the chain…") {
		t.Fatalf("header = %q", h)
	}
}

func TestLogFollowAndPause(t *testing.T) {
	var l logBuffer
	l.follow = true
	for i := range 30 {
		l.add(itoa(i), 10)
	}
	if w := l.window(10); w[9] != "29" {
		t.Fatalf("following window ends at %q", w[9])
	}
	l.scrollBy(-3, 10)
	if l.follow || l.window(10)[9] != "26" {
		t.Fatalf("scroll up: follow %v, window ends %q", l.follow, l.window(10)[9])
	}
	l.add("30", 10)
	if l.window(10)[9] != "26" {
		t.Fatal("a paused view moved when a line arrived")
	}
	l.scrollBy(1<<30, 10)
	if !l.follow || l.window(10)[9] != "30" {
		t.Fatal("scrolling to the bottom did not resume follow")
	}
	l.add("\x1b[32mcolored\x1b[0m", 10)
	if l.lines[len(l.lines)-1] != "colored" {
		t.Fatalf("ANSI kept: %q", l.lines[len(l.lines)-1])
	}
}

func press(m *Model, keys ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range keys {
		_, last = m.Update(keyMsg(k))
	}
	return last
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if k, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(k[0]), Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// runCmd executes a tea.Cmd and feeds its messages back, flattening batches,
// until an actionMsg arrives.
func runAction(t *testing.T, m *Model, cmd tea.Cmd) actionMsg {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case actionMsg:
			m.Update(msg)
			return msg
		}
	}
	t.Fatal("no command ran")
	return actionMsg{}
}

func TestStopNodeConfirmsThenRunsTheShownCommand(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.run = fixtureRunner()
	m.w, m.h = 120, 40
	if cmd := press(m, "s"); cmd != nil || m.overlay != overlayConfirm {
		t.Fatalf("s on a running node: overlay %v, want the confirm", m.overlay)
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, "$ forklab node stop 0 --json") {
		t.Fatalf("confirm does not show the command:\n%s", v)
	}
	msg := runAction(t, m, press(m, "enter"))
	if msg.res.Err != nil || m.last == nil || m.last.Cmd.String() != "forklab node stop 0 --json" {
		t.Fatalf("ran %+v, last %+v", msg.res, m.last)
	}
	if line := ansi.Strip(m.statusLine()); !strings.Contains(line, "$ forklab node stop 0 --json") || !strings.Contains(line, "✓ ok") {
		t.Fatalf("status line = %q", line)
	}

	press(m, "esc", "j")
	if n, _ := m.selectedNode(); n.Name != "node1" {
		t.Fatalf("selected %s, want node1", n.Name)
	}
	if press(m, "s"); m.overlay != overlayNone {
		t.Fatal("s opened a confirm for a node that is not running")
	}
	cmd := press(m, "S")
	if msg := runAction(t, m, cmd); msg.res.Cmd.String() != "forklab node start 1 --json" {
		t.Fatalf("S ran %s", msg.res.Cmd)
	}
}

func TestPaletteRunsRawCommands(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.run = fixtureRunner()
	m.w, m.h = 120, 40
	press(m, ":")
	for _, r := range "exec -- bogus" {
		s := string(r)
		if s == " " {
			s = "space"
		}
		press(m, s)
	}
	msg := runAction(t, m, press(m, "enter"))
	if got := msg.res.Cmd.String(); got != "forklab exec --json -- bogus" {
		t.Fatalf("palette ran %q", got)
	}
	if m.output == nil || m.focus != focusMain {
		t.Fatal("raw command output not shown in the main pane")
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, `unknown command "bogus" for "simd"`) {
		t.Fatalf("exec stderr not rendered:\n%s", v)
	}

	press(m, "esc", ":")
	for _, r := range "status -w" {
		press(m, strings.ReplaceAll(string(r), " ", "space"))
	}
	if cmd := press(m, "enter"); cmd != nil || m.last == nil || m.last.Err == nil {
		t.Fatal("a streaming command was run from the palette")
	}
}

func TestPaletteFuzzyFindsActions(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	press(m, "ctrl+k", "k", "i", "l", "l")
	items := m.paletteItems()
	if len(items) == 0 || items[0].b.id != actNodeKill {
		t.Fatalf("top match for kill = %+v", items)
	}
	if !strings.Contains(items[0].detail, "forklab node kill 0 --json") {
		t.Fatalf("palette entry does not show its command: %q", items[0].detail)
	}
	press(m, "enter")
	if m.overlay != overlayConfirm || m.pending.cmd.String() != "forklab node kill 0 --json" || !m.pending.danger {
		t.Fatalf("palette kill: overlay %v pending %+v", m.overlay, m.pending)
	}
}

func TestPreviewListsViewAndActionCommands(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 140, 40
	press(m, "c")
	v := ansi.Strip(m.render())
	for _, want := range []string{
		"$ forklab status -w --json",
		"$ forklab node logs 0 -f --tail 500 --json",
		"$ forklab node stop 0 --json",
		"$ forklab node restart 0 --json",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	if strings.Contains(v, "node start 0") {
		t.Error("preview offers start for a running node")
	}
	press(m, "esc", "6", "c")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "$ forklab lab down demo --json") || strings.Contains(v, "lab up demo") {
		t.Errorf("labs preview:\n%s", v)
	}
}
