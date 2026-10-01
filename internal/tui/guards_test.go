package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestQuitWaitsForRunningAction(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	m.running = []runningCmd{{cmd: Command{"lab", "down", "demo"}}}
	if cmd := press(m, "q"); cmd != nil {
		t.Fatal("quit did not wait for the running lab down")
	}
	if !strings.Contains(ansi.Strip(m.statusLine()), "q again to force") {
		t.Fatalf("status line = %q", ansi.Strip(m.statusLine()))
	}
	_, cmd := m.Update(actionMsg{res: Result{Cmd: Command{"lab", "down", "demo"}}})
	if cmd == nil {
		t.Fatal("finished action did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("finished action did not quit")
	}
}

func TestChainStreamErrorMeansChainDown(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 140, 40
	feedStream(t, m, streamStatus, "status_w_down.ndjson")
	feedStream(t, m, streamConsensus, "consensus_w_down.ndjson")
	if m.status != nil || m.consensus != nil || m.chainUp() {
		t.Fatalf("status %+v consensus %+v kept after the stream failed", m.status, m.consensus)
	}
	if m.proposals != nil || m.accounts != nil {
		t.Fatal("chain panels still show data from before the failure")
	}
	if h := ansi.Strip(m.header()); strings.Contains(h, "connection refused") || !strings.Contains(h, "Starting the chain…") {
		t.Fatalf("header while the chain has not answered yet: %q", h)
	}
	m.last = nil
	m.quietSince = time.Now().Add(-2 * startGrace)
	if h := ansi.Strip(m.header()); strings.Contains(h, "connection refused") || !strings.Contains(h, "Chain not answering, see the status line") {
		t.Fatalf("header after the grace: %q", h)
	}
	if line := ansi.Strip(m.statusLine()); !strings.Contains(line, "connection refused") {
		t.Fatalf("status line does not carry the failure: %q", line)
	}
	m.poll(true)
	for _, k := range []loadKind{loadProposals, loadAccounts, loadProposal} {
		if m.loads[k].inflight {
			t.Errorf("load %d polled while the chain is down", k)
		}
	}
	if !m.loads[loadNodes].inflight || !m.loads[loadLabs].inflight || !m.loads[loadUpgrade].inflight {
		t.Error("local queries stopped with the chain")
	}
	press(m, "2")
	if v := ansi.Strip(m.render()); !strings.Contains(v, "no node answers: node0") {
		t.Errorf("consensus pane does not show the failure:\n%s", v)
	}
}

func TestStoppedLabCanBeStarted(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	m.setStatus(nil)
	m.loads[loadNodes].seq++
	m.Update(resultMsg{kind: loadNodes, seq: m.loads[loadNodes].seq, res: envelopeData(t, "node_list_down.json")})
	m.loads[loadLabs].seq++
	m.Update(resultMsg{kind: loadLabs, seq: m.loads[loadLabs].seq, res: Result{Data: []byte(`[{"name":"demo","mode":"fresh","validators":2,"running":false}]`)}})
	if len(m.nodes) != 0 {
		t.Fatalf("nodes of a stopped lab still listed: %+v", m.nodes)
	}
	press(m, "6")
	if !labCanStart(m) {
		t.Fatal("u disabled for a stopped lab")
	}
	press(m, "u")
	if m.overlay != overlayConfirm || m.pending.cmd.String() != "forklab lab up demo --json" {
		t.Fatalf("u: overlay %v pending %+v", m.overlay, m.pending)
	}
}

func TestCtrlCClosesOverlays(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	for _, open := range []string{"?", "c", "s", "ctrl+k"} {
		press(m, open)
		if m.overlay == overlayNone {
			t.Fatalf("%s opened nothing", open)
		}
		if cmd := press(m, "ctrl+c"); cmd != nil || m.overlay != overlayNone {
			t.Errorf("ctrl+c in the overlay from %s: overlay %v, quit %v", open, m.overlay, cmd != nil)
		}
	}
}

func TestPaletteReachesOtherPanelsActions(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	press(m, "3", "ctrl+k")
	for _, r := range "stop lab" {
		press(m, strings.ReplaceAll(string(r), " ", "space"))
	}
	items := m.paletteItems()
	if len(items) == 0 || items[0].label != "Labs: Stop lab" {
		t.Fatalf("top item = %+v, want Labs: Stop lab", items)
	}
	press(m, "enter")
	if m.panel != panelLabs || m.overlay != overlayConfirm || m.pending.cmd.String() != "forklab lab down demo --json" {
		t.Fatalf("panel %v overlay %v pending %+v", m.panel, m.overlay, m.pending)
	}
}

func TestMultiLineErrorsStayOnOneRow(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.Update(windowSize([2]int{80, 24}))
	multi := errors.New("rpc status: failed\n\tcaused by: dial tcp\n\n   connection refused")
	m.setStatus(nil)
	m.streams[streamStatus].err = multi
	m.last = &Result{Cmd: Command{"node", "list"}, Err: multi}
	m.loads[loadNodes].err = multi
	mustFit(t, m, "ansi", [2]int{80, 24}, "multi-line error")
	m.last, m.quietSince = nil, time.Now().Add(-2*startGrace)
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	if !strings.Contains(lines[len(lines)-2], "rpc status: failed caused by: dial tcp") {
		t.Errorf("status line = %q", lines[len(lines)-2])
	}
	if !strings.Contains(lines[len(lines)-1], "help") {
		t.Errorf("footer clipped: %q", lines[len(lines)-1])
	}
}

func TestRawCommandGuards(t *testing.T) {
	if got := withoutJSON([]string{"exec", "--json", "--", "status", "--json"}); strings.Join(got, " ") != "exec -- status --json" {
		t.Errorf("withoutJSON = %q, want the chain's --json kept", got)
	}
	for _, a := range []string{"-f", "-w", "--watch=true", "--follow", "-fw", "tui"} {
		if !streams(a) {
			t.Errorf("%q not treated as streaming", a)
		}
	}
	for _, a := range []string{"-h", "--from", "--height", "--in"} {
		if streams(a) {
			t.Errorf("%q treated as streaming", a)
		}
	}
}

func TestStoppedLabDropsChainData(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.logs.follow, m.logs.offset = false, 3
	m.logs.reset("1")
	if !m.logs.follow {
		t.Error("a new log stream starts paused")
	}
	m.Update(streamEvent{
		kind: streamStatus, session: m.streams[streamStatus].session,
		line: []byte(`{"ok":false,"error":{"code":"lab_not_running","message":"no lab running"}}`),
	})
	if m.proposals != nil || m.accounts != nil || m.proposal != nil {
		t.Fatal("chain data of a stopped lab still shown")
	}
}
