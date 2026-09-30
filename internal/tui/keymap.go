package tui

import (
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// action names what a key does. One catalog of bindings drives key
// dispatch, the footer, the help overlay, the palette, and the command
// preview, so none of them can drift from what a key really does.
type action int

const (
	actNone action = iota
	actQuit
	actHelp
	actPalette
	actCommandLine
	actPreview
	actRefresh
	actTheme
	actGoNodes
	actGoConsensus
	actGoProposals
	actGoUpgrades
	actGoAccounts
	actGoLabs
	actGoProfiles
	actGoBinaries
	actNextPanel
	actPrevPanel
	actFocusMain
	actFocusList
	actUp
	actDown
	actTop
	actBottom
	actPageUp
	actPageDown
	actFollow
	actWrap
	actNodeStop
	actNodeStart
	actNodeKill
	actNodeRestart
	actLabUp
	actLabDown
	actClose
	actConfirm
	actOverlayUp
	actOverlayDown
	actRun
)

// scope is where a binding applies. Overlays own the keyboard while open;
// otherwise the focused panel's scope wins over the focus scope, which wins
// over global.
type scope int

const (
	scopeGlobal scope = iota
	scopeList
	scopeMain
	scopeNodes
	scopeLabs
	scopeHelp
	scopePalette
	scopePreview
	scopeConfirm
)

var scopeTitles = map[scope]string{
	scopeGlobal: "Global", scopeList: "Panel list", scopeMain: "Main pane", scopeNodes: "Nodes", scopeLabs: "Labs",
	scopeHelp: "Help", scopePalette: "Palette", scopePreview: "Command preview", scopeConfirm: "Confirm",
}

type binding struct {
	id    action
	keys  []string
	scope scope
	name  string
	// hint is the footer label; footer is its rank there, 0 hides it.
	hint   string
	footer int
	// chord is the footer's key label when the first key reads badly there.
	chord string
	// palette lists the binding in the Ctrl+K palette.
	palette bool
	when    func(*Model) bool
	// cmd is the forklab command the binding runs. A binding has a cmd or a
	// handler, never both.
	cmd func(*Model) Command
	// confirm asks before running cmd; danger colors the prompt red.
	confirm string
	danger  bool
}

func bind(id action, sc scope, name string, keys ...string) binding {
	return binding{id: id, keys: keys, scope: sc, name: name}
}

func (b binding) foot(rank int, hint string) binding {
	b.footer, b.hint = rank, hint
	return b
}

func (b binding) shown(chord string) binding {
	b.chord = chord
	return b
}

// footerKey is the key label the footer shows.
func (b binding) footerKey() string {
	if b.chord != "" {
		return b.chord
	}
	return keyLabel(b.keys[:1])
}

func (b binding) pal() binding {
	b.palette = true
	return b
}

func (b binding) onlyIf(fn func(*Model) bool) binding {
	b.when = fn
	return b
}

func (b binding) runs(fn func(*Model) Command, confirm string, danger bool) binding {
	b.cmd, b.confirm, b.danger = fn, confirm, danger
	b.palette = true
	return b
}

func (b binding) enabled(m *Model) bool { return b.when == nil || b.when(m) }

var catalog = []binding{
	bind(actQuit, scopeGlobal, "Quit", "q", "ctrl+c").foot(90, "quit"),
	bind(actHelp, scopeGlobal, "Help for this panel", "?").foot(80, "help").pal(),
	bind(actPalette, scopeGlobal, "Command palette", "ctrl+k").foot(70, "palette"),
	bind(actCommandLine, scopeGlobal, "Run a forklab command", ":").foot(71, "cmd").pal(),
	bind(actPreview, scopeGlobal, "Command preview", "c").foot(60, "preview").pal(),
	bind(actRefresh, scopeGlobal, "Refresh", "ctrl+r").pal(),
	bind(actTheme, scopeGlobal, "Next theme", "T").pal(),
	bind(actGoNodes, scopeGlobal, "Go to Nodes", "1").pal(),
	bind(actGoConsensus, scopeGlobal, "Go to Consensus", "2").pal(),
	bind(actGoProposals, scopeGlobal, "Go to Proposals", "3").pal(),
	bind(actGoUpgrades, scopeGlobal, "Go to Upgrades", "4").pal(),
	bind(actGoAccounts, scopeGlobal, "Go to Accounts", "5").pal(),
	bind(actGoLabs, scopeGlobal, "Go to Labs", "6").pal(),
	bind(actGoProfiles, scopeGlobal, "Go to Profiles", "7").pal(),
	bind(actGoBinaries, scopeGlobal, "Go to Binaries", "8").pal(),
	bind(actNextPanel, scopeGlobal, "Next panel", "tab"),
	bind(actPrevPanel, scopeGlobal, "Previous panel", "shift+tab"),

	bind(actUp, scopeList, "Up", "k", "up"),
	bind(actDown, scopeList, "Down", "j", "down"),
	bind(actTop, scopeList, "First", "g", "home"),
	bind(actBottom, scopeList, "Last", "G", "end"),
	bind(actFocusMain, scopeList, "Focus the main pane", "enter", "right").foot(50, "focus"),

	bind(actUp, scopeMain, "Scroll up", "k", "up"),
	bind(actDown, scopeMain, "Scroll down", "j", "down"),
	bind(actTop, scopeMain, "Top", "g", "home"),
	bind(actBottom, scopeMain, "Bottom", "G", "end"),
	bind(actPageUp, scopeMain, "Page up", "pgup", "ctrl+u"),
	bind(actPageDown, scopeMain, "Page down", "pgdown", "ctrl+d"),
	bind(actFocusList, scopeMain, "Back to the panel list", "esc", "left").foot(50, "back"),

	bind(actFollow, scopeNodes, "Follow logs", "l").foot(15, "follow").pal().onlyIf(hasNode),
	bind(actWrap, scopeNodes, "Wrap log lines", "w").foot(16, "wrap").pal().onlyIf(hasNode),
	bind(actNodeStop, scopeNodes, "Stop node", "s").foot(10, "stop").onlyIf(nodeRunning).
		runs(nodeCmd("stop"), "Stop %s? It gets SIGTERM.", false),
	bind(actNodeStart, scopeNodes, "Start node", "S").foot(11, "start").onlyIf(nodeDown).
		runs(nodeCmd("start"), "", false),
	bind(actNodeKill, scopeNodes, "Kill node", "K").foot(12, "kill").onlyIf(nodeRunning).
		runs(nodeCmd("kill"), "Kill %s with SIGKILL? This simulates a crash.", true),
	bind(actNodeRestart, scopeNodes, "Restart node", "r").foot(13, "restart").onlyIf(hasNode).
		runs(nodeCmd("restart"), "Restart %s?", false),

	bind(actLabUp, scopeLabs, "Start lab", "u").foot(10, "up").onlyIf(labCanStart).
		runs(labCmd("up"), "Start lab %s?", false),
	bind(actLabDown, scopeLabs, "Stop lab", "d").foot(11, "down").onlyIf(labRunning).
		runs(labCmd("down"), "Stop lab %s and all its nodes?", true),

	bind(actClose, scopeHelp, "Close", "esc", "?", "q", "ctrl+c").foot(10, "close"),
	bind(actUp, scopeHelp, "Scroll up", "k", "up"),
	bind(actDown, scopeHelp, "Scroll down", "j", "down"),

	bind(actClose, scopePalette, "Close", "esc", "ctrl+c").foot(20, "close"),
	bind(actRun, scopePalette, "Run", "enter").foot(10, "run"),
	bind(actOverlayUp, scopePalette, "Previous", "up", "ctrl+p").foot(11, "select").shown("↑↓"),
	bind(actOverlayDown, scopePalette, "Next", "down", "ctrl+n"),

	bind(actClose, scopePreview, "Close", "esc", "c", "q", "ctrl+c").foot(20, "close"),
	bind(actRun, scopePreview, "Run the selected command", "enter").foot(10, "run"),
	bind(actOverlayUp, scopePreview, "Previous", "k", "up").foot(11, "select").shown("↑↓"),
	bind(actOverlayDown, scopePreview, "Next", "j", "down"),

	bind(actConfirm, scopeConfirm, "Confirm", "enter", "y").foot(10, "confirm"),
	bind(actClose, scopeConfirm, "Cancel", "esc", "n", "q", "ctrl+c").foot(11, "cancel"),
}

func nodeCmd(verb string) func(*Model) Command {
	return func(m *Model) Command {
		n, _ := m.selectedNode()
		return Command{"node", verb, itoa(n.Index)}
	}
}

func labCmd(verb string) func(*Model) Command {
	return func(m *Model) Command {
		l, _ := m.selectedLab()
		return Command{"lab", verb, l.Name}
	}
}

func hasNode(m *Model) bool {
	_, ok := m.selectedNode()
	return ok
}

func nodeRunning(m *Model) bool {
	n, ok := m.selectedNode()
	return ok && n.State == "running"
}

func nodeDown(m *Model) bool {
	n, ok := m.selectedNode()
	return ok && n.State != "running"
}

func labRunning(m *Model) bool {
	l, ok := m.selectedLab()
	return ok && l.Running
}

// labCanStart is true for a stopped lab while no other lab runs, since only
// one lab may run at a time.
func labCanStart(m *Model) bool {
	l, ok := m.selectedLab()
	if !ok || l.Running {
		return false
	}
	for _, other := range m.labs {
		if other.Running {
			return false
		}
	}
	return true
}

// scopes lists the active scopes, most specific first.
func (m *Model) scopes() []scope { return m.scopesWith(m.overlay) }

func (m *Model) scopesWith(o overlayKind) []scope {
	switch o {
	case overlayHelp:
		return []scope{scopeHelp}
	case overlayPalette:
		return []scope{scopePalette}
	case overlayPreview:
		return []scope{scopePreview}
	case overlayConfirm:
		return []scope{scopeConfirm}
	}
	var out []scope
	if s, ok := panels[m.panel].scope(); ok {
		out = append(out, s)
	}
	if m.focus == focusMain {
		out = append(out, scopeMain)
	} else {
		out = append(out, scopeList)
	}
	return append(out, scopeGlobal)
}

// match finds the binding key triggers now. A binding whose when is false
// does not match, so its key falls through to a broader scope.
func (m *Model) match(key string) (binding, bool) {
	for _, sc := range m.scopes() {
		for _, b := range catalog {
			if b.scope == sc && hasKey(b.keys, key) && b.enabled(m) {
				return b, true
			}
		}
	}
	return binding{}, false
}

func hasKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// footerBindings are the enabled bindings of the active scopes that rank in
// the footer, in rank order.
func (m *Model) footerBindings() []binding {
	active := map[scope]bool{}
	for _, sc := range m.scopes() {
		active[sc] = true
	}
	var out []binding
	for _, b := range catalog {
		if b.footer > 0 && active[b.scope] && b.enabled(m) {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].footer < out[j].footer })
	return out
}

// helpGroups lists the bindings of the scopes that apply to the focused
// panel, most specific first, with each binding's enabled state.
func (m *Model) helpGroups() []helpGroup {
	scopes := []scope{}
	if s, ok := panels[m.panel].scope(); ok {
		scopes = append(scopes, s)
	}
	scopes = append(scopes, scopeList, scopeMain, scopeGlobal)
	var groups []helpGroup
	for _, sc := range scopes {
		g := helpGroup{title: scopeTitles[sc]}
		if sc == scopeNodes || sc == scopeLabs {
			g.title = panels[m.panel].title
		}
		for _, b := range catalog {
			if b.scope == sc {
				g.rows = append(g.rows, helpRow{keys: keyLabel(b.keys), name: b.name, enabled: b.enabled(m)})
			}
		}
		groups = append(groups, g)
	}
	return groups
}

type helpGroup struct {
	title string
	rows  []helpRow
}

type helpRow struct {
	keys, name string
	enabled    bool
}

func keyLabel(keys []string) string {
	out := make([]string, len(keys))
	for i, k := range keys {
		switch k {
		case "up":
			k = "↑"
		case "down":
			k = "↓"
		case "left":
			k = "←"
		case "right":
			k = "→"
		}
		out[i] = k
	}
	return strings.Join(out, " ")
}

// paletteEntries are every enabled palette action of every panel, the
// focused panel's first, so each action is reachable from anywhere.
func (m *Model) paletteEntries() []binding {
	order := []scope{}
	if s, ok := panels[m.panel].scope(); ok {
		order = append(order, s)
	}
	order = append(order, scopeGlobal)
	for _, p := range panels {
		if s, ok := p.scope(); ok && !slices.Contains(order, s) {
			order = append(order, s)
		}
	}
	var out []binding
	for _, sc := range order {
		for _, b := range catalog {
			if b.scope == sc && b.palette && b.enabled(m) {
				out = append(out, b)
			}
		}
	}
	return out
}

// activeCommands are the enabled command bindings the keyboard reaches now,
// outside any overlay.
func (m *Model) activeCommands() []binding {
	var out []binding
	for _, sc := range m.scopesWith(overlayNone) {
		for _, b := range catalog {
			if b.scope == sc && b.cmd != nil && b.enabled(m) {
				out = append(out, b)
			}
		}
	}
	return out
}

// panelOf is the panel whose scope sc is.
func panelOf(sc scope) (panelID, bool) {
	for p, spec := range panels {
		if s, ok := spec.scope(); ok && s == sc {
			return panelID(p), true
		}
	}
	return 0, false
}

// handlers are the bindings that change the TUI itself rather than run a
// command. init fills it, since overlayRun dispatches through it.
var handlers map[action]func(*Model) tea.Cmd

func init() {
	handlers = map[action]func(*Model) tea.Cmd{
		actQuit:        (*Model).quit,
		actHelp:        func(m *Model) tea.Cmd { m.openOverlay(overlayHelp); return nil },
		actPalette:     func(m *Model) tea.Cmd { m.openPalette(""); return nil },
		actCommandLine: func(m *Model) tea.Cmd { m.openPalette("forklab "); return nil },
		actPreview:     func(m *Model) tea.Cmd { m.openOverlay(overlayPreview); return nil },
		actRefresh:     (*Model).refresh,
		actTheme:       func(m *Model) tea.Cmd { m.th = nextTheme(m.th); return nil },
		actGoNodes:     goPanel(panelNodes),
		actGoConsensus: goPanel(panelConsensus),
		actGoProposals: goPanel(panelProposals),
		actGoUpgrades:  goPanel(panelUpgrades),
		actGoAccounts:  goPanel(panelAccounts),
		actGoLabs:      goPanel(panelLabs),
		actGoProfiles:  goPanel(panelProfiles),
		actGoBinaries:  goPanel(panelBinaries),
		actNextPanel:   func(m *Model) tea.Cmd { return m.setPanel((m.panel + 1) % numPanels) },
		actPrevPanel:   func(m *Model) tea.Cmd { return m.setPanel((m.panel + numPanels - 1) % numPanels) },
		actFocusMain:   func(m *Model) tea.Cmd { m.focus = focusMain; return nil },
		actFocusList:   (*Model).back,
		actUp:          func(m *Model) tea.Cmd { return m.move(-1) },
		actDown:        func(m *Model) tea.Cmd { return m.move(1) },
		actTop:         func(m *Model) tea.Cmd { return m.move(-1 << 30) },
		actBottom:      func(m *Model) tea.Cmd { return m.move(1 << 30) },
		actPageUp:      func(m *Model) tea.Cmd { return m.move(-m.pageSize()) },
		actPageDown:    func(m *Model) tea.Cmd { return m.move(m.pageSize()) },
		actFollow:      (*Model).toggleFollow,
		actWrap:        func(m *Model) tea.Cmd { m.logs.wrap = !m.logs.wrap; return nil },
		actClose:       func(m *Model) tea.Cmd { m.overlay = overlayNone; return nil },
		actConfirm:     (*Model).confirmRun,
		actOverlayUp:   func(m *Model) tea.Cmd { m.overlayMove(-1); return nil },
		actOverlayDown: func(m *Model) tea.Cmd { m.overlayMove(1); return nil },
		actRun:         (*Model).overlayRun,
	}
}

func goPanel(p panelID) func(*Model) tea.Cmd {
	return func(m *Model) tea.Cmd { return m.setPanel(p) }
}
