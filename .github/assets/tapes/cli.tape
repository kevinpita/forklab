# The plain CLI: create a lab, start it, and read its status.
Source tapes/style.tape
Output cli.gif

Type "forklab lab create demo --profile simd --version 0.53.8 --validators 2"
Enter
Wait@60s
Type "forklab lab up demo"
Enter
Wait@120s
Sleep 3s
Type "forklab status"
Enter
Wait
Sleep 1s
Screenshot cli.png
Sleep 500ms
