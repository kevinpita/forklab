package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func failedUpgradeModel(t *testing.T) *Model {
	t.Helper()
	m := loadedModel(t, buildTheme("ansi", true))
	m.setStatus(nil)
	m.upgrade = &upgradeStatus{Lab: "demo", Pending: &pendingUpgrade{Name: "v2", Height: 40, Version: "2"}, Nodes: []upgradeNode{{Name: "node0", State: "exited", Phase: "swapped", Halt: &halt{Height: 40}, SwapError: "failed to unescrow funds"}}}
	return m
}

func TestFailedUpgradeOffersReviewedRecoveryWhileOffline(t *testing.T) {
	m := failedUpgradeModel(t)
	m.w, m.h = 140, 40
	if !strings.Contains(ansi.Strip(m.header()), "U recover") {
		t.Fatal("header hides recovery")
	}
	pane := nodeMain(m, 100, 30)
	if !strings.Contains(ansi.Strip(strings.Join(pane.lines, "\n")), "failed to unescrow funds") {
		t.Fatal("failure hidden by logs")
	}
	press(m, "U")
	if m.form == nil {
		t.Fatal("U did not open recovery")
	}
	press(m, "enter")
	if !m.form.reviewing() || len(m.running) != 0 {
		t.Fatal("recovery must review before executing")
	}
	got := m.form.spec.build(m.form.values()).String()
	if got != "forklab upgrade recover --lab demo --previous --json" {
		t.Fatalf("command %s", got)
	}
	m.upgrade.Lab = "other"
	if got2 := m.form.spec.build(values{"mode": "retry", "version": "3"}).String(); got2 != "forklab upgrade recover --lab demo --version 3 --json" {
		t.Fatalf("form changed target: %s", got2)
	}
}

func TestUpgradeSurvivesRPCFailureButNotLabChange(t *testing.T) {
	m := failedUpgradeModel(t)
	m.setStatus(nil)
	if m.upgrade == nil {
		t.Fatal("lost local recovery state")
	}
	seq := m.loads[loadUpgrade].seq
	m.setStatus(&labStatus{Lab: "other"})
	if m.upgrade != nil || m.loads[loadUpgrade].seq == seq {
		t.Fatal("old lab recovery still visible or in flight")
	}
}

func TestRecoveryViewsFit(t *testing.T) {
	m := failedUpgradeModel(t)
	for _, size := range [][2]int{{180, 50}, {80, 24}, {40, 15}, {12, 8}, {1, 1}} {
		m.w, m.h = size[0], size[1]
		for _, p := range []panelID{panelNodes, panelUpgrades} {
			m.panel = p
			mustFit(t, m, "recovery", size, m.render())
		}
		press(m, "U")
		mustFit(t, m, "recovery", size, m.render())
		press(m, "esc")
	}
}

func TestRecoveryVersionsComeFromFailedLab(t *testing.T) {
	m := failedUpgradeModel(t)
	spec := upgradeRecoverySpec(m)
	choices, err := spec.fields[1].src.parse(json.RawMessage(`[{"name":"other","running":false,"profile":{"binaries":{"wrong":{"path":"/bin/false"}}}},{"name":"demo","running":false,"profile":{"binaries":{"2":{"path":"/bin/true"},"3":{"path":"/bin/true"}}}}]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 {
		t.Fatalf("choices: %+v", choices)
	}
	for _, choice := range choices {
		if choice.value == "wrong" {
			t.Fatal("used another stopped lab's versions")
		}
	}
	m.running = []runningCmd{{cmd: Command{"upgrade", "recover", "--previous"}}}
	if canRecoverUpgrade(m) {
		t.Fatal("duplicate recovery offered while command runs")
	}
}

func TestUpgradeResponseFromPreviousLabIsDiscarded(t *testing.T) {
	m := failedUpgradeModel(t)
	m.setStatus(&labStatus{Lab: "other"})
	m.applyLoad(resultMsg{kind: loadUpgrade, seq: m.loads[loadUpgrade].seq, res: Result{Data: json.RawMessage(`{"lab":"demo","pending":{"name":"old","height":40}}`)}})
	if m.upgrade != nil {
		t.Fatal("old lab's recovery response replaced current lab")
	}
}
