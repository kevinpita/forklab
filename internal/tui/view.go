package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// narrowWidth is the width below which the left column hides and the
// focused area takes the whole body.
const narrowWidth = 60

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "forklab"
	return v
}

func (m *Model) bodyH() int { return max(m.h-3, 0) }

func (m *Model) leftW() int {
	if m.w < narrowWidth {
		return 0
	}
	return min(max(m.w*36/100, 30), 52)
}

// mainRows is how many log lines the Nodes main pane shows.
func (m *Model) mainRows() int { return max(m.bodyH()-5, 1) }

func (m *Model) render() string {
	if m.w <= 0 || m.h <= 0 {
		return "starting forklab…"
	}
	lines := []string{m.header()}
	body := m.body()
	switch m.overlay {
	case overlayHelp:
		body = overlay(body, m.helpView(), m.w)
	case overlayPalette:
		body = overlay(body, m.paletteView(), m.w)
	case overlayPreview:
		body = overlay(body, m.previewView(), m.w)
	case overlayConfirm:
		body = overlay(body, m.confirmView(), m.w)
	case overlayForm:
		body = overlay(body, m.formView(), m.w)
	case overlayProposal:
		body = overlay(body, m.proposalEditorView(), m.w)
	case overlayRecipe:
		body = overlay(body, m.recipeView(), m.w)
	}
	lines = append(lines, body...)
	lines = append(lines, m.statusLine(), m.footer())
	if len(lines) > m.h {
		lines = lines[:m.h]
	}
	for i, l := range lines {
		lines[i] = fit(l, m.w)
	}
	return strings.Join(lines, "\n")
}

// fit clips s to exactly w cells, on one row whatever it holds.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if strings.ContainsAny(s, "\n\r") {
		s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-ansi.StringWidth(s), 0)) + s
}

func (m *Model) header() string {
	th := m.th
	parts := []string{th.Logo.Render("forklab")}
	sep := th.Border.Render(" │ ")
	if m.upgradeNeedsRecovery() {
		return strings.Join(parts, "") + " " + th.Warn.Render(m.recoveryHint())
	}
	s := m.status
	if s == nil {
		var msg string
		switch {
		case m.chainSilent():
			msg = "Chain not answering, see the status line"
		case m.labPhase() == phaseRunning || m.labStarting():
			msg = "Starting the chain…"
		case m.labPhase() == phaseUnknown:
			msg = "loading"
		default:
			msg = m.labHint()
		}
		parts = append(parts, " "+th.Warn.Render("○ "+msg))
		return strings.Join(parts, "")
	}
	// Chips in display order, each with a priority; the least important
	// drop first when the terminal is narrow.
	type chip struct {
		text string
		prio int
	}
	chips := []chip{
		{kv(th, "lab", th.Val.Render(s.Lab)), 0},
		{kv(th, "chain", th.Text.Render(s.ChainID)), 5},
		{kv(th, "h", th.Val.Render(strconv.FormatInt(s.Height, 10))), 1},
	}
	if c := m.consensus; c != nil {
		chips = append(chips, chip{kv(th, "r"+strconv.Itoa(int(c.Round)), th.Accent2.Render(strings.ToUpper(c.Step))), 3})
	}
	if s.AvgBlockTime != "" {
		chips = append(chips, chip{kv(th, "block", th.Text.Render(s.AvgBlockTime)), 4})
	}
	if v := m.versions(); v != "" {
		chips = append(chips, chip{kv(th, "bin", th.Text.Render(v)), 6})
	}
	if s.Pause != nil {
		chips = append(chips, chip{th.Warn.Render(fmt.Sprintf("%s at H=%d", s.Pause.Phase, s.Pause.Height)), 1})
	}
	if p := m.plan(); p != nil {
		chips = append(chips, chip{th.Warn.Render(fmt.Sprintf("⬆ %s @ %d %s", p.Name, p.Height, m.blocksLeft(p.Height))), 2})
	}
	join := func() string {
		parts := make([]string, len(chips))
		for i, c := range chips {
			parts[i] = c.text
		}
		return strings.Join(parts, sep)
	}
	for len(chips) > 1 && ansi.StringWidth(join()) > m.w-22 {
		worst := 0
		for i, c := range chips {
			if c.prio > chips[worst].prio {
				worst = i
			}
		}
		chips = append(chips[:worst], chips[worst+1:]...)
	}
	up := 0
	for _, n := range s.Nodes {
		if n.Up {
			up++
		}
	}
	nodes := th.Good.Render(fmt.Sprintf("● %d/%d", up, len(s.Nodes)))
	if up < len(s.Nodes) {
		nodes = th.Warn.Render(fmt.Sprintf("◐ %d/%d", up, len(s.Nodes)))
	}
	return parts[0] + " " + nodes + sep + join()
}

// versions lists the distinct binary versions the nodes run.
func (m *Model) versions() string {
	var out []string
	for _, n := range m.upgradeNodes() {
		if n.Version != "" && !hasKey(out, n.Version) {
			out = append(out, n.Version)
		}
	}
	if len(out) == 0 {
		for _, l := range m.labs {
			if l.Running {
				return l.Version
			}
		}
	}
	return strings.Join(out, ",")
}

func (m *Model) body() []string {
	h := m.bodyH()
	if h <= 0 {
		return nil
	}
	lw := m.leftW()
	if lw == 0 {
		if m.focus == focusList {
			return m.panelBox(m.panel, m.w, h)
		}
		return m.mainBox(m.w, h)
	}
	left := m.leftColumn(lw, h)
	right := m.mainBox(m.w-lw, h)
	out := make([]string, h)
	for i := range h {
		out[i] = left[i] + right[i]
	}
	return out
}

// leftHeights splits the column between the panels lazygit style: the
// focused panel grows, the others show a few rows, and when space runs out
// they collapse to their title line.
func (m *Model) leftHeights(total int) []int {
	hs := make([]int, numPanels)
	sum := int(numPanels) - 1 + 3
	if sum > total {
		hs[m.panel] = total
		return hs
	}
	for p := range numPanels {
		hs[p] = 1
	}
	hs[m.panel] = 3
	grow := func(p panelID, to int) {
		extra := min(to-hs[p], total-sum)
		if extra > 0 {
			hs[p] += extra
			sum += extra
		}
	}
	need := func(p panelID) int { return max(panels[p].count(m), 1) + 2 }
	// The focused panel lists all it can first; the rest share what is left.
	grow(m.panel, need(m.panel))
	for p := range numPanels {
		if p != m.panel {
			grow(p, min(need(p), 5))
		}
	}
	for p := range numPanels {
		grow(p, need(p))
	}
	hs[m.panel] += total - sum
	return hs
}

func (m *Model) leftColumn(w, h int) []string {
	var out []string
	for p, ph := range m.leftHeights(h) {
		if ph > 0 {
			out = append(out, m.panelBox(panelID(p), w, ph)...)
		}
	}
	return out
}

func (m *Model) panelBox(p panelID, w, h int) []string {
	spec := panels[p]
	active := p == m.panel && m.focus == focusList
	title := fmt.Sprintf("[%d] %s", int(p)+1, spec.title)
	right := spec.summary(m)
	if h == 1 {
		return []string{m.edge(title, right, w, p == m.panel, "─", "─")}
	}
	rows := spec.rows(m)
	inner := max(h-2, 0)
	cur := m.cursor[p]
	start := min(max(cur-inner/2, 0), max(len(rows)-inner, 0))
	lines := make([]string, 0, inner)
	for i := start; i < len(rows) && len(lines) < inner; i++ {
		lines = append(lines, m.listLine(rows[i], w-2, i == cur && p == m.panel, active))
	}
	if len(rows) == 0 && inner > 0 {
		lines = append(lines, m.th.Dim.Render(" "+m.panelEmpty(p)))
	}
	return m.box(title, right, lines, w, h, p == m.panel, active)
}

func (m *Model) panelEmpty(p panelID) string {
	k := map[panelID]loadKind{
		panelNodes: loadNodes, panelProposals: loadProposals, panelUpgrades: loadUpgrade, panelAccounts: loadAccounts,
		panelLabs: loadLabs, panelProfiles: loadProfiles, panelBinaries: loadBinaries,
	}
	if p == panelConsensus && m.labPhase() == phaseRunning {
		if m.streams[streamConsensus].err != nil {
			return "chain not answering"
		}
		return "waiting for the chain"
	}
	if err := m.loads[k[p]].err; err != nil && !m.explained(k[p], err) {
		return "failed to load"
	}
	switch p {
	case panelLabs:
		if m.labPhase() == phaseNoLab {
			return "none yet, n creates one"
		}
	case panelProfiles:
		return "none, n creates one"
	case panelBinaries:
		return "none cached, f fetches one"
	default:
		switch m.labPhase() {
		case phaseNoLab:
			return "no lab yet, n creates one"
		case phaseStopped:
			return "lab stopped, u starts it"
		}
		if m.status == nil {
			return "waiting for the chain"
		}
	}
	return "none"
}

// labHint is what a panel about the chain says while no lab runs.
func (m *Model) labHint() string {
	switch m.labPhase() {
	case phaseNoLab:
		return "No lab yet. Press n to create one"
	case phaseStopped:
		if l, ok := m.selectedLab(); ok {
			return "Lab " + l.Name + " is stopped. Press u to start it"
		}
		return "No lab running. Pick one in [6] Labs and press u"
	case phaseUnknown:
		return "loading…"
	}
	return "waiting for the chain…"
}

func (m *Model) listLine(r listRow, w int, selected, focused bool) string {
	th := m.th
	if selected && focused {
		plain := " " + r.glyph + " " + ansi.Strip(r.text)
		aside := ansi.Strip(r.aside)
		gap := max(w-ansi.StringWidth(plain)-ansi.StringWidth(aside)-1, 1)
		return th.Sel.Render(fit(plain+strings.Repeat(" ", gap)+aside+" ", w))
	}
	marker := " "
	text := r.text
	if selected {
		marker = th.SelDim.Render("▌")
		text = th.SelDim.Render(ansi.Strip(r.text))
	}
	left := marker + r.style.Render(r.glyph) + " " + text
	aside := th.Dim.Render(r.aside)
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(aside) - 1
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + aside + " "
}

func (m *Model) mainW() int {
	if lw := m.leftW(); lw > 0 {
		return m.w - lw
	}
	return m.w
}

func (m *Model) mainView(w, inner int) mainView {
	if m.output != nil {
		return m.outputView()
	}
	return panels[m.panel].main(m, w-4, inner)
}

func (m *Model) mainBox(w, h int) []string {
	inner := max(h-2, 0)
	v := m.mainView(w, inner)
	lines := v.lines
	scroll := min(m.scroll, max(len(lines)-inner, 0))
	if !v.tail {
		lines = lines[min(scroll, len(lines)):]
	}
	for i, l := range lines {
		lines[i] = " " + l
	}
	right := v.right
	if !v.tail && len(v.lines) > inner && inner > 0 {
		right += m.th.Dim.Render(fmt.Sprintf("  %d/%d", scroll+1, len(v.lines)-inner+1))
	}
	return m.box(v.title, right, lines, w, h, true, m.focus == focusMain || m.leftW() == 0)
}

func (m *Model) outputView() mainView {
	r, th := m.output, m.th
	var lines []string
	right := th.StatusOK.Render("✓ ok")
	if r.Err != nil {
		right = th.Error.Render("✗ " + r.Err.Error())
		lines = append(lines, th.Bad.Render(r.Err.Error()), "")
	}
	var ex execResult
	if len(r.Cmd) > 0 && r.Cmd[0] == "exec" && json.Unmarshal(r.Data, &ex) == nil && ex.Args != nil {
		lines = append(lines, kv(th, "args", strings.Join(ex.Args, " ")), kv(th, "exit", strconv.Itoa(ex.ExitCode)), "")
		for _, l := range strings.Split(strings.TrimRight(ex.Stdout, "\n"), "\n") {
			lines = append(lines, th.Text.Render(l))
		}
		if ex.Stderr != "" {
			lines = append(lines, "", th.Title.Render("stderr"))
			for _, l := range strings.Split(strings.TrimRight(ex.Stderr, "\n"), "\n") {
				lines = append(lines, th.Bad.Render(l))
			}
		}
	} else if len(r.Data) > 0 {
		var buf bytes.Buffer
		if json.Indent(&buf, r.Data, "", "  ") == nil {
			for _, l := range strings.Split(buf.String(), "\n") {
				lines = append(lines, th.Text.Render(l))
			}
		}
	}
	return mainView{title: "$ " + r.Cmd.String(), right: right + th.Dim.Render("  esc close"), lines: lines}
}

// box draws a rounded frame with title and right label set into its top
// edge, and pads or clips the content to fit.
func (m *Model) box(title, right string, content []string, w, h int, current, active bool) []string {
	if w < 4 || h < 2 {
		out := make([]string, h)
		for i := range out {
			if i < len(content) {
				out[i] = fit(content[i], w)
			} else {
				out[i] = fit("", w)
			}
		}
		return out
	}
	bs := m.th.Border
	if active {
		bs = m.th.BorderActive
	}
	out := []string{m.edgeStyled(title, right, w, current, active, "╭", "╮")}
	for i := range h - 2 {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		out = append(out, bs.Render("│")+fit(line, w-2)+bs.Render("│"))
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

func (m *Model) edge(title, right string, w int, current bool, l, r string) string {
	return m.edgeStyled(title, right, w, current, false, l, r)
}

func (m *Model) edgeStyled(title, right string, w int, current, active bool, l, r string) string {
	bs, ts := m.th.Border, m.th.Dim
	switch {
	case active:
		bs, ts = m.th.BorderActive, m.th.TitleActive
	case current:
		ts = m.th.Title
	}
	t := " " + title + " "
	if ansi.StringWidth(t) > w-4 {
		t = ansi.Truncate(t, max(w-4, 0), "…")
	}
	rt := ""
	if right != "" {
		rt = " " + right + " "
	}
	room := w - 3 - ansi.StringWidth(t)
	if ansi.StringWidth(rt) > room-1 {
		rt = ""
	}
	fill := max(w-3-ansi.StringWidth(t)-ansi.StringWidth(rt), 0)
	return bs.Render(l+"─") + ts.Render(t) + bs.Render(strings.Repeat("─", fill)) + m.th.Dim.Render(rt) + bs.Render(r)
}

func (m *Model) quitNote() string {
	if !m.quitting {
		return ""
	}
	return m.th.Warn.Render("  quitting when this finishes, q again to force")
}

func (m *Model) statusLine() string {
	th := m.th
	if err := m.streams[streamStatus].err; err != nil && len(m.running) == 0 && m.chainSilent() {
		return th.FooterKey.Render(" $ ") + th.Text.Render(Command{"status", "-w"}.String()) + "  " + th.Error.Render("✗ "+oneLine(err.Error()))
	}
	if len(m.running) > 0 {
		r := m.running[len(m.running)-1]
		took := th.Dim.Render(" " + fmtDur(time.Since(r.at).Round(time.Second)))
		more := ""
		if n := len(m.running) - 1; n > 0 {
			more = th.Dim.Render(fmt.Sprintf(" +%d more", n))
		}
		activity := ""
		if r.progress.current.Message != "" {
			activity = th.Title.Render(r.progress.current.Message) + th.Dim.Render(" · ")
		}
		return th.Val.Render(spinFrame(m.spin)) + " " + activity + th.FooterKey.Render("$ ") + th.Text.Render(r.cmd.String()) + took + more + m.quitNote()
	}
	if m.last == nil {
		return th.Dim.Render(" $ ready · every action runs a forklab command, c shows them")
	}
	r := m.last
	left := th.FooterKey.Render(" $ ") + th.Text.Render(r.Cmd.String())
	var right string
	if r.Err != nil {
		right = th.Error.Render("✗ "+oneLine(r.Err.Error())) + th.Dim.Render(" "+fmtTook(r))
	} else {
		right = th.StatusOK.Render("✓ ok") + th.Dim.Render(" "+fmtTook(r))
	}
	// A long command gives way so the outcome always shows.
	room := m.w - ansi.StringWidth(right) - 3
	if ansi.StringWidth(left) > room {
		left = ansi.Truncate(left, max(room, 0), "…")
	}
	gap := max(m.w-ansi.StringWidth(left)-ansi.StringWidth(right)-1, 2)
	return left + strings.Repeat(" ", gap) + right
}

func spinFrame(i int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[i%len(frames)]
}

func fmtTook(r *Result) string {
	if r.Took == 0 {
		return ""
	}
	if r.Took < 1e9 {
		return strconv.FormatInt(r.Took.Milliseconds(), 10) + "ms"
	}
	return fmt.Sprintf("%.1fs", r.Took.Seconds())
}

func (m *Model) footer() string {
	th := m.th
	var parts []string
	help := ""
	for _, b := range m.footerBindings() {
		hint := th.FooterKey.Render(b.footerKey()) + " " + th.FooterDesc.Render(b.hint)
		if b.id == actHelp {
			help = hint
			continue
		}
		parts = append(parts, hint)
	}
	if help != "" {
		parts = append(parts, help)
	}
	out := " " + strings.Join(parts, "  ")
	if ansi.StringWidth(out) <= m.w || help == "" {
		return out
	}
	// Whole hints that fit, then help, which stays reachable.
	room := m.w - ansi.StringWidth(help) - 5
	out = ""
	for _, p := range parts[:len(parts)-1] {
		if ansi.StringWidth(out)+2+ansi.StringWidth(p) > room {
			break
		}
		out += "  " + p
	}
	return strings.TrimPrefix(out, " ") + "  … " + help
}

// overlay splices the modal into the middle of body.
func overlay(body, modal []string, w int) []string {
	if len(body) == 0 {
		return body
	}
	mw := 0
	for _, l := range modal {
		mw = max(mw, ansi.StringWidth(l))
	}
	mw = min(mw, w)
	if len(modal) > len(body) {
		modal = modal[:len(body)]
	}
	top := (len(body) - len(modal)) / 2
	x := max((w-mw)/2, 0)
	out := append([]string(nil), body...)
	for i, l := range modal {
		row := fit(out[top+i], w)
		out[top+i] = ansi.Truncate(row, x, "") + fit(l, mw) + ansi.TruncateLeft(row, x+mw, "")
	}
	return out
}

// modal frames lines in a box sized to the screen.
func (m *Model) modal(title, right string, lines []string, want int) []string {
	w := min(want, m.w-2)
	h := min(len(lines)+2, max(m.bodyH(), 0))
	if h < 3 || w < 8 {
		return nil
	}
	return m.box(title, right, lines, w, h, true, true)
}

func (m *Model) helpLines() []string {
	th := m.th
	var lines []string
	for _, g := range m.helpGroups() {
		if len(g.rows) == 0 {
			continue
		}
		lines = append(lines, " "+th.Accent2.Render(g.title))
		for _, r := range g.rows {
			key, name := th.FooterKey.Render(padRight(r.keys, 14)), th.Text.Render(r.name)
			if !r.enabled {
				key, name = th.Dim.Render(padRight(r.keys, 14)), th.Dim.Render(r.name+" (not now)")
			}
			lines = append(lines, " "+key+name)
		}
		lines = append(lines, "")
	}
	return lines
}

func (m *Model) helpRoom() int { return max(m.bodyH()-2, 1) }

func (m *Model) helpView() []string {
	lines := m.helpLines()
	off := min(m.helpOff, max(len(lines)-m.helpRoom(), 0))
	title := panels[m.panel].title
	if m.helpFrom == overlayRecipe {
		title = scopeTitles[scopeRecipe]
	}
	return m.modal("Help · "+title, "j/k scroll · esc close", lines[off:], 64)
}

// clampScroll keeps the main pane and help offsets inside their content,
// so scrolling back starts moving at once. View only reads them.
func (m *Model) clampScroll() {
	inner := max(m.bodyH()-2, 0)
	m.scroll = max(min(m.scroll, len(m.mainView(m.mainW(), inner).lines)-inner), 0)
	if m.overlay == overlayHelp {
		m.helpOff = max(min(m.helpOff, len(m.helpLines())-m.helpRoom()), 0)
	}
}

type paletteItem struct {
	label, detail string
	b             binding
	raw           []string
}

// paletteItems ranks the palette's actions against the query. A query that
// starts with "forklab" is a raw command; any other query also offers to run
// itself as one.
func (m *Model) paletteItems() []paletteItem {
	q := strings.TrimSpace(m.input.Value())
	if rest, ok := strings.CutPrefix(q, "forklab"); ok && (rest == "" || rest[0] == ' ') {
		args, err := splitArgs(rest)
		if err != nil || len(args) == 0 || !knownCommand(args) {
			return nil
		}
		args = withoutJSON(args)
		return []paletteItem{{label: "Run", detail: Command(args).String(), raw: args}}
	}
	var items []paletteItem
	for _, b := range m.paletteEntries() {
		label := b.name
		if p, ok := panelOf(b.scope); ok {
			label = panels[p].title + ": " + label
		}
		it := paletteItem{label: label, detail: keyLabel(b.keys), b: b}
		switch {
		case b.form != nil:
			it.detail += "  form: " + b.command(m).String()
		case b.cmd != nil:
			it.detail += "  " + b.cmd(m).String()
		}
		items = append(items, it)
	}
	items = fuzzyRank(items, q, func(it paletteItem) string { return it.label })
	if q != "" {
		if args, err := splitArgs(q); err == nil && knownCommand(args) {
			args = withoutJSON(args)
			items = append(items, paletteItem{label: "Run", detail: Command(args).String(), raw: args})
		}
	}
	return items
}

func withoutJSON(args []string) []string {
	out := args[:0:0]
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		if a != "--json" {
			out = append(out, a)
		}
	}
	return out
}

func (m *Model) paletteW() int { return min(max(m.w*2/3, 40), 90) }

func (m *Model) paletteView() []string {
	th := m.th
	w := m.paletteW()
	items := m.paletteItems()
	lines := []string{m.input.View(), th.Border.Render(strings.Repeat("─", max(w-2, 0)))}
	room := max(min(m.bodyH()-4, 14), 1)
	cur := min(m.palette.cursor, max(len(items)-1, 0))
	start := max(cur-room+1, 0)
	for i := start; i < len(items) && i < start+room; i++ {
		it := items[i]
		label := padRight(it.label, 24)
		if i == cur {
			lines = append(lines, th.Sel.Render(fit(" "+label+" "+ansi.Strip(it.detail), w-2)))
			continue
		}
		lines = append(lines, " "+th.Text.Render(label)+" "+th.Dim.Render(it.detail))
	}
	if len(items) == 0 {
		lines = append(lines, th.Dim.Render(" no match"))
	}
	return m.modal("Command palette", "type forklab … to run any command", lines, w)
}

type previewItem struct {
	label string
	cmd   Command
	b     *binding
}

// previewItems are the commands behind the current view and every action
// available on the selection.
func (m *Model) previewItems() []previewItem {
	items := []previewItem{
		{label: "header", cmd: Command{"status", "-w"}},
		{label: "view", cmd: panels[m.panel].view(m)},
	}
	if m.output != nil {
		items[1].cmd = m.output.Cmd
	}
	for _, b := range m.activeCommands() {
		items = append(items, previewItem{label: b.keys[0] + " " + strings.ToLower(b.name), cmd: b.command(m), b: &b})
	}
	return items
}

func (m *Model) previewView() []string {
	th := m.th
	items := m.previewItems()
	w := min(max(m.w*2/3, 44), 96)
	var lines []string
	cur := min(m.preview, len(items)-1)
	// A long label takes its own line and a long command wraps, so no
	// command is cut short.
	const indent = 18
	for i, it := range items {
		label := " " + padRight(it.label, 14) + " "
		var rows []string
		if ansi.StringWidth(label) > indent {
			rows, label = append(rows, label), strings.Repeat(" ", indent-1)
		}
		for j, part := range wrapWords("$ "+it.cmd.String(), max(w-3-indent, 8)) {
			if j > 0 {
				label, part = strings.Repeat(" ", indent-1), "  "+part
			}
			rows = append(rows, label+part)
		}
		for _, r := range rows {
			switch {
			case i == cur:
				lines = append(lines, th.Sel.Render(fit(r, w-2)))
			case it.b == nil:
				lines = append(lines, th.Dim.Render(r))
			default:
				lines = append(lines, th.FooterKey.Render(r))
			}
		}
	}
	lines = append(lines, "", th.Dim.Render(" every screen and action is one of these commands; enter runs an action"))
	return m.modal("forklab commands", panels[m.panel].title, lines, w)
}

func (m *Model) confirmView() []string {
	p := m.pending
	if p == nil {
		return nil
	}
	th := m.th
	title := th.Warn.Render(p.prompt)
	if p.danger {
		title = th.Bad.Render(p.prompt)
	}
	lines := []string{
		" " + title,
		"",
		" " + th.FooterKey.Render("$ ") + th.Val.Render(p.cmd.String()),
		"",
		" " + th.FooterKey.Render("enter") + th.Dim.Render(" run   ") + th.FooterKey.Render("esc") + th.Dim.Render(" cancel"),
	}
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	return m.modal("Confirm", "", lines, w+4)
}

// wrapWords breaks s at spaces into lines of at most w cells, so a flag
// such as --json never splits; only a word longer than w is cut.
func wrapWords(s string, w int) []string {
	var out []string
	line := ""
	var words []string
	for _, word := range strings.Split(s, " ") {
		for ansi.StringWidth(word) > w {
			words = append(words, ansi.Truncate(word, w, ""))
			word = ansi.TruncateLeft(word, w, "")
		}
		words = append(words, word)
	}
	for _, word := range words {
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	return append(out, line)
}

// oneLine collapses a possibly multi-line message to one line of single
// spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
