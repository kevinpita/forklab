package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// A form collects the arguments of one forklab command. Its spec is data:
// fields and a pure build function from their values to the argv, so the
// preview line, the run, and the tests all read the same command.

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldNumber
	fieldSelect
	fieldToggle
)

type option struct{ value, label string }

// source loads a select's options with a forklab command. cmd returns nil
// while the field it depends on has no value yet.
type source struct {
	cmd   func(v values) Command
	parse func(data json.RawMessage, v values) ([]option, error)
}

type fieldSpec struct {
	key, label, hint string
	kind             fieldKind
	// def is the starting value; a select keeps it once its options load.
	def         string
	options     []option
	src         *source
	optional    bool
	placeholder string
	// show hides the field unless it holds for the values before it.
	show  func(v values) bool
	check func(s string, v values) string
}

// values are the visible fields' values by key; a toggle is "true" or "".
type values map[string]string

type formSpec struct {
	title  string
	fields []fieldSpec
	build  func(v values) Command
	// stepped shows one field at a time and a review step before running.
	stepped bool
	// show puts the result in the main pane.
	show bool
	// then is offered in a confirm once the command succeeds.
	then func(v values) *pendingRun
}

type field struct {
	fieldSpec
	input   textinput.Model
	opts    []option
	choice  int
	on      bool
	touched bool
}

type optionLoad struct {
	done bool
	data json.RawMessage
	err  error
}

type form struct {
	id     int
	spec   *formSpec
	fields []field
	focus  int
	// step is the stepped form's position; len(visible) is the review.
	step    int
	loads   map[string]*optionLoad
	running Command
	started time.Time
	err     error
}

type formOptionsMsg struct {
	form int
	key  string
	res  Result
}

func newForm(th Theme, id int, spec *formSpec) *form {
	f := &form{id: id, spec: spec, loads: map[string]*optionLoad{}}
	for _, s := range spec.fields {
		fl := field{fieldSpec: s, opts: s.options}
		switch s.kind {
		case fieldText, fieldNumber:
			fl.input = newFieldInput(th, s.def)
			fl.input.Placeholder = s.placeholder
		case fieldToggle:
			fl.on = s.def == "true"
		case fieldSelect:
			fl.choice = optionIndex(fl.opts, s.def)
		}
		f.fields = append(f.fields, fl)
	}
	f.focus = f.visible()[0]
	f.focusInput()
	return f
}

func newFieldInput(th Theme, value string) textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	st := textinput.DefaultStyles(th.Dark)
	st.Focused.Text, st.Blurred.Text = th.Val, th.Text
	st.Focused.Placeholder, st.Blurred.Placeholder = th.Dim, th.Dim
	st.Cursor.Color = th.P.Accent
	st.Cursor.Blink = false
	in.SetStyles(st)
	in.SetValue(value)
	in.CursorEnd()
	return in
}

func optionIndex(opts []option, value string) int {
	for i, o := range opts {
		if o.value == value {
			return i
		}
	}
	return 0
}

func (fl *field) value() string {
	switch fl.kind {
	case fieldToggle:
		if fl.on {
			return "true"
		}
		return ""
	case fieldSelect:
		if len(fl.opts) == 0 {
			if fl.src != nil {
				return fl.def
			}
			return ""
		}
		return fl.opts[min(fl.choice, len(fl.opts)-1)].value
	}
	return strings.TrimSpace(fl.input.Value())
}

// values walks the fields in order; a field whose show is false for the
// values before it is left out, so build never sees a hidden field.
func (f *form) values() values {
	v := values{}
	for i := range f.fields {
		fl := &f.fields[i]
		if fl.show != nil && !fl.show(v) {
			continue
		}
		if s := fl.value(); s != "" {
			v[fl.key] = s
		}
	}
	return v
}

func (f *form) visible() []int {
	v := values{}
	var out []int
	for i := range f.fields {
		fl := &f.fields[i]
		if fl.show != nil && !fl.show(v) {
			continue
		}
		out = append(out, i)
		if s := fl.value(); s != "" {
			v[fl.key] = s
		}
	}
	return out
}

// problem is what is wrong with field i, or "".
func (f *form) problem(i int, v values) string {
	fl := &f.fields[i]
	s := v[fl.key]
	if fl.kind == fieldSelect && fl.src != nil {
		if src := f.optionState(fl, v); src != nil && src.err != nil {
			return oneLine(src.err.Error())
		}
		if len(fl.opts) == 0 && !fl.optional {
			if src := f.optionState(fl, v); src != nil && !src.done {
				return "loading options"
			}
			return "nothing to choose from"
		}
	}
	if s == "" {
		if fl.optional || fl.kind == fieldToggle {
			return ""
		}
		return "required"
	}
	if fl.kind == fieldNumber {
		if p := checkDigits(s, v); p != "" {
			return p
		}
	}
	if fl.check != nil {
		return fl.check(s, v)
	}
	return ""
}

func (f *form) optionState(fl *field, v values) *optionLoad {
	if fl.src == nil {
		return nil
	}
	c := fl.src.cmd(v)
	if c == nil {
		return nil
	}
	return f.loads[c.String()]
}

// command is the argv the form runs now. An empty required field shows as
// its key in capitals, so the preview reads like a usage line.
func (f *form) command() Command {
	v := f.values()
	for _, i := range f.visible() {
		fl := &f.fields[i]
		if _, ok := v[fl.key]; !ok && !fl.optional && fl.kind != fieldToggle {
			v[fl.key] = strings.ToUpper(fl.key)
		}
	}
	return f.spec.build(v)
}

// valid reports whether every visible field passes, marking them touched
// so their problems show.
func (f *form) valid() bool {
	v := f.values()
	ok := true
	for _, i := range f.visible() {
		f.fields[i].touched = true
		if f.problem(i, v) != "" {
			if ok {
				f.setFocus(i)
			}
			ok = false
		}
	}
	return ok
}

func (f *form) setFocus(i int) {
	f.fields[f.focus].input.Blur()
	f.focus = i
	f.focusInput()
}

func (f *form) focusInput() {
	fl := &f.fields[f.focus]
	if fl.kind == fieldText || fl.kind == fieldNumber {
		fl.input.Focus()
	}
}

// move steps the focus through the visible fields, wrapping.
func (f *form) move(delta int) {
	vis := f.visible()
	f.fields[f.focus].touched = true
	at := 0
	for k, i := range vis {
		if i == f.focus {
			at = k
		}
	}
	f.setFocus(vis[(at+delta+len(vis))%len(vis)])
}

// stepCount counts the fields every run asks plus the review; a field shown
// only for some choice shares the step of the field before it, so the total
// never changes as choices do.
func (f *form) stepCount() int {
	n := 1
	for _, fl := range f.fields {
		if fl.show == nil {
			n++
		}
	}
	return n
}

func (f *form) stepNumber() int {
	n := 0
	for _, i := range f.visible() {
		if f.fields[i].show == nil {
			n++
		}
		if i == f.focus {
			break
		}
	}
	return max(n, 1)
}

// reviewing is true on the stepped form's last step.
func (f *form) reviewing() bool { return f.spec.stepped && f.step >= len(f.visible()) }

func (f *form) focused() *field {
	if f.reviewing() {
		return nil
	}
	return &f.fields[f.focus]
}

// advance moves a stepped form to its next step once the current field
// passes, and reports whether the form is ready to run.
func (f *form) advance() bool {
	if !f.spec.stepped {
		return true
	}
	if f.reviewing() {
		return true
	}
	fl := &f.fields[f.focus]
	fl.touched = true
	if f.problem(f.focus, f.values()) != "" {
		return false
	}
	f.step++
	if vis := f.visible(); f.step < len(vis) {
		f.setFocus(vis[f.step])
	} else {
		f.fields[f.focus].input.Blur()
	}
	return false
}

func (f *form) back() {
	if !f.spec.stepped {
		f.move(-1)
		return
	}
	f.step = max(f.step-1, 0)
	f.setFocus(f.visible()[f.step])
}

func (f *form) cycle(delta int) {
	fl := f.focused()
	if fl == nil {
		return
	}
	switch fl.kind {
	case fieldToggle:
		fl.on = !fl.on
	case fieldSelect:
		if n := len(fl.opts); n > 0 {
			fl.choice = (fl.choice + delta + n) % n
		}
	}
	fl.touched = true
}

// sync reparses every select's options for the current values and starts
// the loads it lacks. A select keeps its value when the new options have it.
func (f *form) sync(run func(Command) tea.Cmd) tea.Cmd {
	var cmds []tea.Cmd
	for pass := 0; pass < len(f.fields); pass++ {
		changed := false
		v := values{}
		for i := range f.fields {
			fl := &f.fields[i]
			if fl.show != nil && !fl.show(v) {
				continue
			}
			if fl.src != nil {
				if c := fl.src.cmd(v); c != nil {
					key := c.String()
					st := f.loads[key]
					if st == nil {
						st = &optionLoad{}
						f.loads[key] = st
						cmds = append(cmds, run(c))
					}
					if st.done {
						want := fl.value()
						var opts []option
						if st.err == nil {
							opts, st.err = fl.src.parse(st.data, v)
						}
						if !slices.Equal(opts, fl.opts) {
							fl.opts, fl.choice = opts, optionIndex(opts, want)
							changed = true
						}
					}
				} else if len(fl.opts) > 0 {
					fl.opts, fl.choice = nil, 0
					changed = true
				}
			}
			if s := fl.value(); s != "" {
				v[fl.key] = s
			}
		}
		if !changed {
			break
		}
	}
	return tea.Batch(cmds...)
}

// Model side

func (m *Model) openForm(spec *formSpec) tea.Cmd {
	m.formSeq++
	m.form = newForm(m.th, m.formSeq, spec)
	m.openOverlay(overlayForm)
	m.sizeForm()
	return m.syncForm()
}

func (m *Model) sizeForm() {
	if m.form == nil {
		return
	}
	w := max(min(m.formW(), m.w-2)-formLabelW-6, 4)
	for i := range m.form.fields {
		in := &m.form.fields[i].input
		in.SetWidth(w)
		// Setting the cursor again scrolls the text to the new width.
		in.SetCursor(in.Position())
	}
}

func (m *Model) syncForm() tea.Cmd {
	f := m.form
	if f == nil {
		return nil
	}
	id, run, ctx := f.id, m.run, m.ctx
	return f.sync(func(c Command) tea.Cmd {
		return func() tea.Msg { return formOptionsMsg{form: id, key: c.String(), res: run.Run(ctx, c)} }
	})
}

func (m *Model) applyFormOptions(msg formOptionsMsg) tea.Cmd {
	f := m.form
	if f == nil || f.id != msg.form {
		return nil
	}
	st := f.loads[msg.key]
	if st == nil {
		return nil
	}
	st.done, st.data, st.err = true, msg.res.Data, msg.res.Err
	return m.syncForm()
}

// formKey types into the focused text field.
func (m *Model) formKey(msg tea.Msg) tea.Cmd {
	f := m.form
	fl := f.focused()
	if f.running != nil || fl == nil || (fl.kind != fieldText && fl.kind != fieldNumber) {
		return nil
	}
	var cmd tea.Cmd
	fl.input, cmd = fl.input.Update(msg)
	return tea.Batch(cmd, m.syncForm())
}

func (m *Model) formCycle(delta int) tea.Cmd {
	m.form.cycle(delta)
	return m.syncForm()
}

func (m *Model) formSubmit() tea.Cmd {
	f := m.form
	if !f.advance() {
		return m.syncForm()
	}
	if !f.valid() {
		if f.spec.stepped {
			f.step = max(slices.Index(f.visible(), f.focus), 0)
		}
		return nil
	}
	c := f.command()
	f.running, f.started, f.err = c, time.Now(), nil
	return m.exec(c, f.spec.show)
}

// closeForm cancels an idle form; a running one keeps running in the
// background and reports in the status line.
func (m *Model) closeForm() tea.Cmd {
	m.overlay = overlayNone
	if m.form != nil && m.form.running == nil {
		m.form = nil
	}
	return nil
}

// formDone routes a finished command to the form that ran it.
func (m *Model) formDone(res Result) {
	f := m.form
	if f == nil || f.running == nil || f.running.String() != res.Cmd.String() {
		return
	}
	f.running = nil
	if m.overlay != overlayForm {
		m.form = nil
		return
	}
	if res.Err != nil {
		f.err = res.Err
		return
	}
	v := f.values()
	m.form, m.overlay = nil, overlayNone
	if f.spec.then != nil {
		if p := f.spec.then(v); p != nil {
			m.pending = p
			m.openOverlay(overlayConfirm)
		}
	}
}

func formIdle(m *Model) bool { return m.form != nil && m.form.running == nil }

func formOnChoice(m *Model) bool {
	if !formIdle(m) {
		return false
	}
	fl := m.form.focused()
	return fl != nil && (fl.kind == fieldSelect || fl.kind == fieldToggle)
}

// View

// formLabelW fits the longest label, 14 cells, a space, and the marker.
const formLabelW = 17

func (m *Model) formW() int { return min(max(m.w*2/3, 56), 100) }

func (m *Model) formView() []string {
	f := m.form
	if f == nil {
		return nil
	}
	th := m.th
	w := m.formW()
	inner := max(min(w, m.w-2)-2, 1)
	v := f.values()
	var lines []string
	focusLine := 0
	row := func(i int) {
		fl := &f.fields[i]
		label := padRight(fl.label, formLabelW-2)
		mark := "  "
		ls := th.Key
		if i == f.focus && !f.reviewing() {
			mark, ls = th.Accent2.Render("› "), th.Title
			focusLine = len(lines)
		}
		lines = append(lines, " "+mark+ls.Render(label)+m.fieldWidget(fl, i == f.focus && !f.reviewing()))
		prob := ""
		if fl.touched {
			prob = f.problem(i, v)
		}
		switch {
		case prob != "":
			lines = append(lines, "   "+strings.Repeat(" ", formLabelW-2)+th.Bad.Render("✗ "+prob))
		case i == f.focus && fl.hint != "":
			lines = append(lines, "   "+strings.Repeat(" ", formLabelW-2)+th.Dim.Render(fl.hint))
		}
	}
	vis := f.visible()
	right := ""
	switch {
	case f.reviewing():
		right = "step " + itoa(f.stepCount()) + " of " + itoa(f.stepCount()) + " · review"
		lines = append(lines, " "+th.Title.Render("Review"), "")
		for _, i := range vis {
			fl := &f.fields[i]
			val := fl.value()
			if fl.kind == fieldSelect && len(fl.opts) > 0 {
				val = fl.opts[min(fl.choice, len(fl.opts)-1)].label
			}
			if val == "" {
				val = th.Dim.Render("(default)")
			}
			lines = append(lines, "   "+th.Key.Render(padRight(fl.label, formLabelW-2))+th.Text.Render(val))
		}
	case f.spec.stepped:
		right = "step " + itoa(f.stepNumber()) + " of " + itoa(f.stepCount())
		row(f.focus)
	default:
		for _, i := range vis {
			row(i)
		}
	}
	// Keep the focused field in view when the fields outgrow the screen.
	tail := m.formTail(inner)
	room := max(m.bodyH()-2-len(tail), 1)
	if len(lines) > room {
		start := min(max(focusLine-room/2, 0), len(lines)-room)
		lines = lines[start : start+room]
	}
	return m.modal(f.spec.title, right, append(lines, tail...), w)
}

// formTail is the rule, the live command, and the run state under the
// fields.
func (m *Model) formTail(inner int) []string {
	f, th := m.form, m.th
	out := []string{th.Border.Render(strings.Repeat("─", inner))}
	cmd := f.command().String()
	if f.running != nil {
		cmd = f.running.String()
	}
	for i, l := range wrapWords("$ "+cmd, max(inner-3, 1)) {
		if i == 0 {
			l = strings.TrimPrefix(l, "$ ")
			out = append(out, " "+th.FooterKey.Render("$ ")+th.Val.Render(l))
			continue
		}
		out = append(out, "   "+th.Val.Render(l))
	}
	switch {
	case f.running != nil:
		out = append(out, " "+th.Val.Render(spinFrame(m.spin))+" "+th.Text.Render("running "+fmtDur(time.Since(f.started).Round(time.Second)))+
			th.Dim.Render(" · esc keeps it running in the background"))
	case f.err != nil:
		ls := wrapWords("✗ "+oneLine(f.err.Error()), max(inner-2, 1))
		for _, l := range ls[:min(len(ls), 3)] {
			out = append(out, " "+th.Bad.Render(l))
		}
	}
	return out
}

func (m *Model) fieldWidget(fl *field, focused bool) string {
	th := m.th
	switch fl.kind {
	case fieldToggle:
		if fl.on {
			return th.Good.Render("[x]") + th.Dim.Render(" on")
		}
		return th.Dim.Render("[ ] off")
	case fieldSelect:
		if len(fl.opts) == 0 {
			if st := m.form.optionState(fl, m.form.values()); st != nil && !st.done {
				return th.Dim.Render("loading…")
			}
			return th.Dim.Render("none")
		}
		o := fl.opts[min(fl.choice, len(fl.opts)-1)]
		if !focused {
			return th.Text.Render(o.label)
		}
		return th.Accent2.Render("‹ ") + th.Val.Render(o.label) + th.Accent2.Render(" ›") +
			th.Dim.Render("  "+itoa(fl.choice+1)+"/"+itoa(len(fl.opts)))
	}
	return fl.input.View()
}
