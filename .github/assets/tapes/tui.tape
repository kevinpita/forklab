# Still screenshots of the TUI on lab demo, which render.sh left running
# with one passed proposal and one in its voting period.
Source tapes/style.tape
Output tui.gif

Hide
Type "forklab"
Enter
Sleep 5s
Show

Sleep 1s
Screenshot tui-main.png

Type "2"
Sleep 1s
Wait+Screen@15s /step Precommit/
Screenshot tui-consensus.png

Type "3"
Sleep 1s
Down
Sleep 2s
Screenshot tui-proposals.png

Type "1"
Sleep 1s
Ctrl+K
Sleep 500ms
Type "node"
Sleep 1s
Screenshot tui-palette.png
Escape

Sleep 500ms
Type "c"
Sleep 1s
Screenshot tui-preview.png
Escape

Type "q"
Sleep 500ms
