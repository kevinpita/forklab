// Package tui is forklab's terminal UI. It reads and changes everything by
// running `forklab <cmd> --json` as a subprocess, so every screen and action
// has a CLI equivalent that the UI shows.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type focusArea int

const (
	focusList focusArea = iota
	focusMain
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayHelp
	overlayPalette
	overlayPreview
	overlayConfirm
	overlayForm
	overlayRecipe
)

// loadKind names one polled query. Each has its own sequence so a slow
// answer never overwrites a newer one.
type loadKind int

const (
	loadNodes loadKind = iota
	loadProposals
	loadProposal
	loadUpgrade
	loadAccounts
	loadLabs
	loadProfiles
	loadProfile
	loadBinaries
	numLoads
)

type loadState struct {
	seq      int
	inflight bool
	at       time.Time
	err      error
}

type streamState struct {
	session int
	cancel  context.CancelFunc
	// target is what the stream follows, such as the node index for logs.
	target string
	err    error
}

const (
	tickEvery    = time.Second
	restartAfter = 2 * time.Second
	logCap       = 4000
	// logTail is how much history a log stream starts with.
	logTail = 500
)

// Model is the root Bubble Tea model. Panels are plain data plus render
// functions; Model routes keys by overlay, focus, and panel.
type Model struct {
	ctx   context.Context
	run   Runner
	th    Theme
	w, h  int
	focus focusArea
	panel panelID

	overlay  overlayKind
	cursor   [numPanels]int
	scroll   int // main pane scroll for detail views
	helpOff  int
	helpFrom overlayKind
	palette  paletteState
	input    textinput.Model
	preview  int
	pending  *pendingRun
	form     *form
	recipe   *recipeEditor
	formSeq  int
	quitting bool
	spinning bool
	spin     int

	loads   [numLoads]loadState
	streams [numStreams]streamState
	events  chan streamEvent
	session int

	status    *labStatus
	consensus *consensusState
	nodes     []nodeInfo
	proposals []proposal
	proposal  *proposal
	upgrade   *upgradeStatus
	accounts  []account
	labs      []labInfo
	profiles  []profileInfo
	profile   *profileInfo
	binaries  []binaryInfo
	logs      logBuffer
	// labsKnown is true while the last lab list loaded; until then no
	// panel can tell a missing lab from a slow one.
	labsKnown bool
	// wizardOffered is set once the first-lab wizard opened; it opens by
	// itself once per session, and n reopens it.
	wizardOffered bool
	// streamLab is the running lab the chain streams were last pointed at.
	streamLab string
	// quietSince is when a running lab's chain last stopped answering.
	quietSince time.Time

	running []runningCmd
	last    *Result
	// output is the result of a command run from the palette, shown in the
	// main pane until the user moves on.
	output *Result
}

type paletteState struct {
	cursor int
}

type runningCmd struct {
	cmd Command
	at  time.Time
}

// pendingRun is an action waiting in the confirm overlay.
type pendingRun struct {
	cmd    Command
	prompt string
	danger bool
	show   bool
}

type (
	resultMsg struct {
		kind loadKind
		seq  int
		res  Result
	}
	actionMsg struct {
		res Result
		// show puts the result in the main pane.
		show bool
	}
	tickMsg    struct{}
	spinMsg    struct{}
	restartMsg struct {
		kind    streamKind
		session int
	}
)

// New builds the model. ctx bounds every subprocess the TUI starts.
func New(ctx context.Context, run Runner, th Theme) *Model {
	return &Model{ctx: ctx, run: run, th: th, events: make(chan streamEvent, 512), logs: logBuffer{follow: true}}
}

func (m *Model) Init() tea.Cmd {
	m.startStream(streamStatus, "")
	m.startStream(streamConsensus, "")
	return tea.Batch(m.waitStream(), m.poll(true), tick())
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.clampScroll()
	if m.status != nil || (m.labPhase() != phaseRunning && !m.labStarting()) {
		m.quietSince = time.Time{}
	} else if m.quietSince.IsZero() {
		m.quietSince = time.Now()
	}
	return m, cmd
}

// startGrace is how long a lab may take to answer before the header calls
// it silent; a fresh lab needs a few blocks first.
const startGrace = 45 * time.Second

// chainSilent is true once a running lab has not answered for startGrace.
func (m *Model) chainSilent() bool {
	return !m.quietSince.IsZero() && time.Since(m.quietSince) > startGrace && m.streams[streamStatus].err != nil
}

// labStarting is true while a lab up runs.
func (m *Model) labStarting() bool {
	for _, r := range m.running {
		if len(r.cmd) > 1 && r.cmd[0] == "lab" && r.cmd[1] == "up" {
			return true
		}
	}
	return false
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.sizeForm()
		return nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.PasteMsg:
		if m.overlay == overlayForm {
			return m.formKey(msg)
		}
		if m.overlay == overlayPalette {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.palette.cursor = 0
			return cmd
		}
		return nil
	case tickMsg:
		return tea.Batch(m.poll(false), m.followLogs(), tick())
	case spinMsg:
		if len(m.running) == 0 {
			m.spinning = false
			return nil
		}
		m.spin++
		return spinTick()
	case resultMsg:
		return m.applyLoad(msg)
	case actionMsg:
		return m.applyAction(msg)
	case formOptionsMsg:
		return m.applyFormOptions(msg)
	case streamEvent:
		return m.applyStream(msg)
	case restartMsg:
		st := m.streams[msg.kind]
		if st.session != msg.session {
			return nil
		}
		m.startStream(msg.kind, st.target)
		return nil
	}
	return nil
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	b, ok := m.match(key)
	if !ok {
		if m.overlay == overlayForm {
			return m.formKey(msg)
		}
		if m.overlay == overlayPalette {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.palette.cursor = 0
			return cmd
		}
		return nil
	}
	return m.trigger(b)
}

// trigger does what b does: runs its command, opens its form, or calls
// its handler.
func (m *Model) trigger(b binding) tea.Cmd {
	switch {
	case b.cmd != nil:
		return m.request(b)
	case b.form != nil:
		return m.openForm(b.form(m))
	}
	return handlers[b.id](m)
}

func (m *Model) closeOverlay() tea.Cmd {
	if m.overlay == overlayForm {
		return m.closeForm()
	}
	if m.overlay == overlayHelp {
		m.overlay, m.helpFrom = m.helpFrom, overlayNone
	} else {
		m.overlay = overlayNone
	}
	return nil
}

func (m *Model) openHelp() tea.Cmd {
	m.helpFrom = m.overlay
	m.openOverlay(overlayHelp)
	return nil
}

func (m *Model) quit() tea.Cmd {
	// A running action such as lab down must not die half done, so the first
	// quit waits for it and a second one forces.
	if len(m.running) > 0 && !m.quitting {
		m.quitting = true
		return nil
	}
	for i := range m.streams {
		if c := m.streams[i].cancel; c != nil {
			c()
		}
	}
	return tea.Quit
}

func (m *Model) openOverlay(o overlayKind) {
	m.overlay, m.helpOff, m.preview = o, 0, 0
}

func (m *Model) openPalette(query string) {
	m.openOverlay(overlayPalette)
	m.palette = paletteState{}
	m.input = newInput(m.th, query)
	m.input.SetWidth(max(m.paletteW()-6, 1))
}

func (m *Model) back() tea.Cmd {
	m.output, m.focus = nil, focusList
	return nil
}

func (m *Model) setPanel(p panelID) tea.Cmd {
	if p != m.panel {
		m.scroll = 0
	}
	m.panel, m.output = p, nil
	return tea.Batch(m.poll(false), m.followLogs())
}

// move steps the list cursor, or scrolls the main pane when it has focus.
func (m *Model) move(delta int) tea.Cmd {
	if m.overlay == overlayHelp {
		m.helpOff = max(0, m.helpOff+delta)
		return nil
	}
	if m.focus == focusMain {
		if m.output == nil && m.panel == panelNodes {
			m.logs.scrollBy(delta, m.mainRows())
			return nil
		}
		m.scroll = max(0, m.scroll+delta)
		return nil
	}
	n := panels[m.panel].count(m)
	c := min(max(m.cursor[m.panel]+delta, 0), max(n-1, 0))
	if c == m.cursor[m.panel] {
		return nil
	}
	m.cursor[m.panel], m.scroll = c, 0
	return tea.Batch(m.poll(false), m.followLogs())
}

func (m *Model) pageSize() int { return max(1, m.mainRows()-1) }

func (m *Model) toggleFollow() tea.Cmd {
	m.logs.follow = !m.logs.follow
	if m.logs.follow {
		m.logs.offset = 0
	}
	return m.followLogs()
}

func (m *Model) overlayMove(delta int) {
	switch m.overlay {
	case overlayPalette:
		n := len(m.paletteItems())
		m.palette.cursor = min(max(m.palette.cursor+delta, 0), max(n-1, 0))
	case overlayPreview:
		n := len(m.previewItems())
		m.preview = min(max(m.preview+delta, 0), max(n-1, 0))
	}
}

// overlayRun runs what the palette or preview has selected.
func (m *Model) overlayRun() tea.Cmd {
	switch m.overlay {
	case overlayPalette:
		items := m.paletteItems()
		if len(items) == 0 {
			return nil
		}
		it := items[min(m.palette.cursor, len(items)-1)]
		m.overlay = overlayNone
		if it.raw != nil {
			return m.runRaw(it.raw)
		}
		// An action of another panel acts on that panel's selection, so the
		// panel comes into view first.
		var show tea.Cmd
		if p, ok := panelOf(it.b.scope); ok && p != m.panel {
			show = m.setPanel(p)
		}
		return tea.Batch(show, m.trigger(it.b))
	case overlayPreview:
		items := m.previewItems()
		if len(items) == 0 {
			return nil
		}
		it := items[min(m.preview, len(items)-1)]
		m.overlay = overlayNone
		if it.b == nil {
			return nil
		}
		return m.trigger(*it.b)
	}
	return nil
}

// request runs a command binding, through the confirm overlay when it asks
// for one.
func (m *Model) request(b binding) tea.Cmd {
	c := b.cmd(m)
	if b.confirm != "" {
		m.pending = &pendingRun{cmd: c, prompt: fmt.Sprintf(b.confirm, m.selectionName()), danger: b.danger, show: b.show}
		m.openOverlay(overlayConfirm)
		return nil
	}
	return m.exec(c, b.show)
}

func (m *Model) confirmRun() tea.Cmd {
	p := m.pending
	m.overlay, m.pending = overlayNone, nil
	if p == nil {
		return nil
	}
	return m.exec(p.cmd, p.show)
}

// runRaw runs a command typed into the palette. Streaming flags are refused
// because the result pane shows one answer, and the panels already stream.
func (m *Model) runRaw(args []string) tea.Cmd {
	c := Command(args)
	for _, a := range args {
		if a == "--" {
			break
		}
		if streams(a) {
			m.last = &Result{Cmd: c, Err: errors.New("streaming commands run in their panels (logs, status, consensus)")}
			return nil
		}
	}
	return m.exec(c, true)
}

// streams reports whether a raw argument makes a command run until canceled.
func streams(a string) bool {
	if a == "tui" || strings.HasPrefix(a, "--watch") || strings.HasPrefix(a, "--follow") {
		return true
	}
	return len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a[1:], "fw")
}

func (m *Model) exec(c Command, show bool) tea.Cmd {
	m.running = append(m.running, runningCmd{cmd: c, at: time.Now()})
	// Actions outlive a canceled context so a quit never kills one half done.
	run, ctx := m.run, context.WithoutCancel(m.ctx)
	cmds := []tea.Cmd{func() tea.Msg { return actionMsg{res: run.Run(ctx, c), show: show} }}
	if !m.spinning {
		m.spinning = true
		cmds = append(cmds, spinTick())
	}
	return tea.Batch(cmds...)
}

func spinTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *Model) applyAction(msg actionMsg) tea.Cmd {
	for i, r := range m.running {
		if r.cmd.String() == msg.res.Cmd.String() {
			m.running = append(m.running[:i:i], m.running[i+1:]...)
			break
		}
	}
	res := msg.res
	m.last = &res
	m.formDone(res)
	m.recipeDone(res)
	if m.quitting && len(m.running) == 0 {
		return m.quit()
	}
	if msg.show {
		m.output, m.focus, m.scroll = &res, focusMain, 0
	}
	// Anything may have changed, so every query is due again.
	for k := range m.loads {
		m.loads[k].at = time.Time{}
	}
	return m.poll(false)
}

func (m *Model) refresh() tea.Cmd {
	for k := range m.loads {
		m.loads[k].at = time.Time{}
	}
	for k := range numStreams {
		if k != streamLogs || m.streams[k].cancel != nil {
			m.startStream(k, m.streams[k].target)
		}
	}
	return m.poll(false)
}

// load starts one query unless the same kind is already in flight.
func (m *Model) load(k loadKind, c Command) tea.Cmd {
	st := &m.loads[k]
	if st.inflight {
		return nil
	}
	st.seq++
	st.inflight, st.at = true, time.Now()
	seq, run, ctx := st.seq, m.run, m.ctx
	return func() tea.Msg { return resultMsg{kind: k, seq: seq, res: run.Run(ctx, c)} }
}

// due reports whether k was last loaded more than every ago.
func (m *Model) due(k loadKind, every time.Duration) bool {
	return time.Since(m.loads[k].at) >= every
}

// poll starts the queries that are due. Chain queries wait for a running
// lab; the cheap local ones always run. all ignores the schedule.
func (m *Model) poll(all bool) tea.Cmd {
	var cmds []tea.Cmd
	want := func(k loadKind, every time.Duration, c Command) {
		if all || m.due(k, every) {
			cmds = append(cmds, m.load(k, c))
		}
	}
	want(loadLabs, 5*time.Second, Command{"lab", "list"})
	want(loadNodes, 2*time.Second, Command{"node", "list"})
	want(loadProfiles, 20*time.Second, Command{"profile", "list"})
	want(loadBinaries, 20*time.Second, Command{"binary", "list"})
	if m.chainUp() {
		focused := func(p panelID, slow, fast time.Duration) time.Duration {
			if m.panel == p {
				return fast
			}
			return slow
		}
		want(loadUpgrade, focused(panelUpgrades, 10*time.Second, 3*time.Second), Command{"upgrade", "status"})
		want(loadProposals, focused(panelProposals, 15*time.Second, 4*time.Second), Command{"gov", "list"})
		want(loadAccounts, focused(panelAccounts, 30*time.Second, 6*time.Second), Command{"account", "list"})
		if p, ok := m.selectedProposal(); ok && m.panel == panelProposals &&
			(m.proposal == nil || m.proposal.ID != p.ID || m.due(loadProposal, 3*time.Second)) {
			cmds = append(cmds, m.load(loadProposal, Command{"gov", "show", strconv.FormatUint(p.ID, 10)}))
		}
	}
	if p, ok := m.selectedProfile(); ok && m.panel == panelProfiles &&
		(m.profile == nil || m.profile.Name != p.Name || m.due(loadProfile, 10*time.Second)) {
		cmds = append(cmds, m.load(loadProfile, Command{"profile", "show", p.Name}))
	}
	return tea.Batch(cmds...)
}

// chainUp is true while a lab runs, as far as the status stream knows.
func (m *Model) chainUp() bool { return m.status != nil }

func (m *Model) applyLoad(msg resultMsg) tea.Cmd {
	st := &m.loads[msg.kind]
	if msg.seq != st.seq {
		return nil
	}
	prev := st.err
	st.inflight, st.err = false, msg.res.Err
	if msg.res.Err != nil {
		// A failed query shows no data, never the last answer as if current.
		// An error the lab's state explains gets friendly text in the panel;
		// any other one goes to the status line once, when it first appears.
		m.clearLoad(msg.kind)
		if !m.explained(msg.kind, msg.res.Err) && (prev == nil || prev.Error() != msg.res.Err.Error()) {
			res := msg.res
			m.last = &res
		}
		m.clampCursors()
		return m.followLogs()
	}
	var err error
	data := msg.res.Data
	switch msg.kind {
	case loadNodes:
		err = decodeInto(data, &m.nodes)
	case loadProposals:
		err = decodeInto(data, &m.proposals)
	case loadProposal:
		m.proposal = &proposal{}
		err = json.Unmarshal(data, m.proposal)
	case loadUpgrade:
		m.upgrade = &upgradeStatus{}
		err = json.Unmarshal(data, m.upgrade)
	case loadAccounts:
		err = decodeInto(data, &m.accounts)
	case loadLabs:
		err = decodeInto(data, &m.labs)
		m.labsKnown = err == nil
		m.followRunningLab()
	case loadProfiles:
		err = decodeInto(data, &m.profiles)
	case loadProfile:
		m.profile = &profileInfo{}
		err = json.Unmarshal(data, m.profile)
	case loadBinaries:
		err = decodeInto(data, &m.binaries)
	}
	st.err = err
	m.clampCursors()
	return tea.Batch(m.followLogs(), m.offerWizard())
}

// followRunningLab restarts the chain streams when another lab starts
// running: a stream resolves its lab once, and a new lab reuses the ports
// of the old one, so an old stream would show the new chain as the old lab.
func (m *Model) followRunningLab() {
	running := ""
	for _, l := range m.labs {
		if l.Running {
			running = l.Name
		}
	}
	if running == m.streamLab {
		return
	}
	m.streamLab = running
	if running == "" || (m.status != nil && m.status.Lab == running) {
		return
	}
	m.setStatus(nil)
	m.consensus = nil
	m.startStream(streamStatus, "")
	m.startStream(streamConsensus, "")
}

// offerWizard opens the first-lab wizard the first time the lab list turns
// out empty while nothing else holds the screen. Later an empty list shows
// the empty state, so deleting the last lab never traps the user in it.
func (m *Model) offerWizard() tea.Cmd {
	if m.labPhase() != phaseNoLab || m.wizardOffered || m.overlay != overlayNone {
		return nil
	}
	m.wizardOffered = true
	return m.openForm(labCreateSpec(m, true))
}

// labPhase is what the TUI knows about labs: whether any exist and run.
type labPhase int

const (
	phaseUnknown labPhase = iota
	phaseNoLab
	phaseStopped
	phaseRunning
)

func (m *Model) labPhase() labPhase {
	switch {
	case !m.labsKnown:
		return phaseUnknown
	case len(m.labs) == 0:
		return phaseNoLab
	}
	for _, l := range m.labs {
		if l.Running {
			return phaseRunning
		}
	}
	return phaseStopped
}

// explained reports whether the lab's state accounts for a failed query:
// the queries about a lab fail while none exists or runs. The TUI's own
// queries are well formed, so a usage error from one means it found no lab
// to ask, as when a lab stops before the lab list says so.
func (m *Model) explained(k loadKind, err error) bool {
	if isLabNotRunning(err) {
		return true
	}
	switch k {
	case loadNodes, loadProposals, loadProposal, loadUpgrade, loadAccounts:
		var ce *CLIError
		return m.labPhase() != phaseRunning || errors.As(err, &ce) && ce.Code == "usage"
	}
	return false
}

func decodeInto[T any](data json.RawMessage, dst *[]T) error {
	var v []T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*dst = v
	return nil
}

func (m *Model) clampCursors() {
	for p := range numPanels {
		m.cursor[p] = min(m.cursor[p], max(panels[p].count(m)-1, 0))
	}
}

// startStream (re)starts a stream under a new session; events from the old
// one are dropped by session.
func (m *Model) startStream(k streamKind, target string) {
	st := &m.streams[k]
	if st.cancel != nil {
		st.cancel()
	}
	m.session++
	ctx, cancel := context.WithCancel(m.ctx)
	*st = streamState{session: m.session, cancel: cancel, target: target, err: st.err}
	var c Command
	switch k {
	case streamStatus:
		c = Command{"status", "-w"}
	case streamConsensus:
		c = Command{"consensus", "-w"}
	case streamLogs:
		c = logsCmd(target)
		m.logs.reset(target)
	}
	run, ch, session := m.run, m.events, m.session
	go run.Stream(ctx, c, k, session, ch)
}

func (m *Model) waitStream() tea.Cmd {
	ch := m.events
	return func() tea.Msg { return <-ch }
}

// applyStream handles one event and every event already queued behind it,
// so a burst of log lines costs one render.
func (m *Model) applyStream(ev streamEvent) tea.Cmd {
	var cmds []tea.Cmd
	// The cap keeps a flood of lines from holding Update forever.
	for range cap(m.events) {
		if c := m.streamEvent(ev); c != nil {
			cmds = append(cmds, c)
		}
		select {
		case ev = <-m.events:
			continue
		default:
		}
		break
	}
	return tea.Batch(append(cmds, m.waitStream())...)
}

func (m *Model) streamEvent(ev streamEvent) tea.Cmd {
	st := &m.streams[ev.kind]
	if ev.session != st.session {
		return nil
	}
	if ev.done {
		if ev.err != nil {
			st.err = ev.err
			m.chainDown(ev.kind)
		}
		session := ev.session
		return tea.Tick(restartAfter, func(time.Time) tea.Msg { return restartMsg{kind: ev.kind, session: session} })
	}
	var err error
	switch ev.kind {
	case streamStatus:
		var s labStatus
		if err = decodeLine(ev.line, &s); err == nil {
			m.setStatus(&s)
		}
	case streamConsensus:
		var c consensusState
		if err = decodeLine(ev.line, &c); err == nil {
			m.consensus = &c
		}
	case streamLogs:
		var l logLine
		if err = decodeLine(ev.line, &l); err == nil {
			m.logs.add(l.Line, m.mainRows())
		}
	}
	st.err = err
	if err != nil {
		m.chainDown(ev.kind)
	}
	return nil
}

// chainDown drops what a failing status or consensus stream last said: a
// chain that stopped answering must not look live. Without a status the
// chain queries stop too, until the stream recovers.
func (m *Model) chainDown(k streamKind) {
	switch k {
	case streamStatus:
		m.setStatus(nil)
	case streamConsensus:
		m.consensus = nil
	}
}

// clearLoad forgets the data of a failed query.
func (m *Model) clearLoad(k loadKind) {
	switch k {
	case loadNodes:
		m.nodes = nil
	case loadProposals:
		m.proposals = nil
	case loadProposal:
		m.proposal = nil
	case loadUpgrade:
		m.upgrade = nil
	case loadAccounts:
		m.accounts = nil
	case loadLabs:
		m.labs, m.labsKnown = nil, false
	case loadProfiles:
		m.profiles = nil
	case loadProfile:
		m.profile = nil
	case loadBinaries:
		m.binaries = nil
	}
}

// setStatus records the chain status and drops the chain data of a lab
// that stopped or was replaced, so no panel shows it as live.
func (m *Model) setStatus(s *labStatus) {
	if s == nil || m.status == nil || s.Lab != m.status.Lab {
		for _, k := range []loadKind{loadProposals, loadProposal, loadAccounts, loadUpgrade} {
			m.clearLoad(k)
			m.loads[k].at, m.loads[k].err = time.Time{}, nil
		}
		m.clampCursors()
	}
	m.status = s
}

// followLogs keeps the log stream on the selected node while the Nodes panel
// shows, and stops it otherwise.
func (m *Model) followLogs() tea.Cmd {
	st := &m.streams[streamLogs]
	n, ok := m.selectedNode()
	if m.panel != panelNodes || !ok {
		if st.cancel != nil {
			st.cancel()
			*st = streamState{target: st.target}
		}
		return nil
	}
	target := itoa(n.Index)
	if st.cancel != nil && st.target == target {
		return nil
	}
	m.startStream(streamLogs, target)
	return nil
}

func (m *Model) selectedNode() (nodeInfo, bool) {
	return pick(m.nodes, m.cursor[panelNodes])
}

func (m *Model) selectedLab() (labInfo, bool) { return pick(m.labs, m.cursor[panelLabs]) }

func (m *Model) selectedProposal() (proposal, bool) {
	return pick(m.sortedProposals(), m.cursor[panelProposals])
}

func (m *Model) selectedProfile() (profileInfo, bool) {
	return pick(m.profiles, m.cursor[panelProfiles])
}

func pick[T any](items []T, i int) (T, bool) {
	var zero T
	if i < 0 || i >= len(items) {
		return zero, false
	}
	return items[i], true
}

// selectionName names the selection for confirm prompts.
func (m *Model) selectionName() string {
	switch m.panel {
	case panelNodes:
		n, _ := m.selectedNode()
		return n.Name
	case panelLabs:
		l, _ := m.selectedLab()
		return l.Name
	case panelProfiles:
		p, _ := m.selectedProfile()
		return p.Name
	case panelUpgrades:
		if p := m.plan(); p != nil {
			return p.Name
		}
	}
	return ""
}

func logsCmd(node string) Command {
	return Command{"node", "logs", node, "-f", "--tail", itoa(logTail)}
}

func itoa(i int) string { return strconv.Itoa(i) }
