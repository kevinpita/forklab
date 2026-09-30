# The first-run wizard: bare forklab opens the TUI, which offers to create
# the first lab while none exists. render.sh runs this before any lab exists
# and skips the shot when the wizard never shows up.
Source tapes/style.tape
Output wizard.gif

Hide
Type "forklab"
Enter
Show

Wait+Screen@20s /first lab/
Sleep 1s
Screenshot wizard.png

Hide
Ctrl+C
Sleep 500ms
