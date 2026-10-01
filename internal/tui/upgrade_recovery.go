package tui

import (
	"encoding/json"
	"fmt"
	"time"
)

func (m *Model) invalidateUpgrade() {
	m.upgrade = nil
	st := &m.loads[loadUpgrade]
	st.seq++
	st.inflight, st.at, st.err = false, time.Time{}, nil
}

func (m *Model) upgradeNeedsRecovery() bool {
	if m.upgrade == nil || m.upgrade.Pending == nil {
		return false
	}
	if m.upgrade.Pending.Recovery != nil {
		return true
	}
	for _, n := range m.upgrade.Nodes {
		if n.SwapError != "" || n.Phase == "swap_failed" || (n.Halt != nil && n.State != "running") {
			return true
		}
	}
	return false
}

func canRecoverUpgrade(m *Model) bool {
	if !m.upgradeNeedsRecovery() {
		return false
	}
	for _, r := range m.running {
		if len(r.cmd) > 0 && r.cmd[0] == "upgrade" {
			return false
		}
	}
	for _, n := range m.upgrade.Nodes {
		if n.Phase == "swapping" {
			return false
		}
	}
	return true
}

func (m *Model) recoveryHint() string {
	p := m.upgrade.Pending
	if !canRecoverUpgrade(m) {
		return fmt.Sprintf("%s · upgrade %s · recovery in progress", m.upgrade.Lab, p.Name)
	}
	return fmt.Sprintf("%s · upgrade %s needs attention · U recover", m.upgrade.Lab, p.Name)
}

func upgradeRecoverySpec(m *Model) *formSpec {
	u := m.upgrade
	p := u.Pending
	lab := u.Lab
	versions := &source{
		cmd: func(values) Command { return Command{"lab", "list"} },
		parse: func(data json.RawMessage, _ values) ([]option, error) {
			var labs []struct {
				labInfo
				Profile profileDoc `json:"profile"`
			}
			if err := json.Unmarshal(data, &labs); err != nil {
				return nil, err
			}
			for _, l := range labs {
				if l.Name == lab {
					return versionOptions(l.Profile, nil), nil
				}
			}
			return nil, fmt.Errorf("lab %s is unavailable", lab)
		},
	}
	return &formSpec{
		title:   fmt.Sprintf("Recover %s · %s at %d", lab, p.Name, p.Height),
		stepped: true,
		fields: []fieldSpec{
			{
				key: "mode", label: "Recovery", kind: fieldSelect, def: "previous", listChoices: true,
				options: []option{{"previous", "Previous binary + skip this upgrade"}, {"retry", "Retry with a selected version"}},
				hint:    "Switches executables and keeps existing chain data.",
				choiceHints: map[string]string{
					"previous": fmt.Sprintf("Restart all nodes on their previous binaries and skip height %d. This does not restore chain state.", p.Height),
					"retry":    "Restart all nodes on the selected version and retry the pending upgrade.",
				},
			},
			{key: "version", label: "Version", kind: fieldSelect, src: versions, def: p.Version, show: has("mode", "retry")},
		},
		build: func(v values) Command {
			c := Command{"upgrade", "recover", "--lab", lab}
			if v["mode"] == "retry" {
				return append(c, "--version", v["version"])
			}
			return append(c, "--previous")
		},
	}
}
