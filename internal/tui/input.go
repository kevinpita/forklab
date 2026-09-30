package tui

import (
	"charm.land/bubbles/v2/textinput"
)

// newInput is the themed single-line input the palette and the command line
// type into, focused with value in it and the cursor at the end.
func newInput(th Theme, value string) textinput.Model {
	in := textinput.New()
	in.Prompt = "› "
	st := textinput.DefaultStyles(th.Dark)
	st.Focused.Prompt = th.Accent2
	st.Focused.Text = th.Text
	st.Focused.Placeholder = th.Dim
	st.Cursor.Color = th.P.Accent
	st.Cursor.Blink = false
	in.SetStyles(st)
	in.Placeholder = "search actions, or type forklab …"
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	return in
}
