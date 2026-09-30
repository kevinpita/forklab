# Schedules the 11.1.1 -> 11.2.0 upgrade on lab xrp from the Upgrades form
# with auto swap, then captures the halt and the swapped nodes.
Source tapes/style.tape
Output upgrade.gif

Hide
Type "forklab"
Enter
Sleep 5s
Type "4"
Sleep 1s
Show

Type "u"
Wait+Screen@20s /upgrade schedule 11\.2\.0/
Sleep 1s
Screenshot upgrade-form.png
Sleep 1s
Enter
Sleep 3s
Escape

Wait+Screen@180s /running +halted|swapping/
Sleep 300ms
Screenshot upgrade-halt.png
Wait+Screen@180s /swapped[\s\S]*swapped/
Sleep 4s
Screenshot upgrade-swap.png

Hide
Type "q"
Sleep 500ms
