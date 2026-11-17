# Records docs/demo.gif. Run docs/demo/setup.sh first, then from the repo
# root: vhs docs/demo/demo.tape
#
# The stack is the Docker integration suite's: a private network
# passphrase, test keys, and acme-b's test sudo password (op-sudo-pw).

Output docs/demo.gif
Require bin/auriga

Set Shell "bash"
Set FontSize 15
Set Width 1000
Set Height 620
Set Padding 12
Set TypingSpeed 60ms
Set Theme "Builtin Dark"

Hide
Type "export XDG_CONFIG_HOME=$PWD/test/integration/.work/demo-state/config XDG_STATE_HOME=$PWD/test/integration/.work/demo-state/state PATH=$PWD/bin:$PATH"
Enter
Type `alias auriga='auriga --test-network-passphrase "Auriga Integration Network ; November 2026"'; PS1='$ '; clear`
Enter
Show

Type "auriga" Sleep 400ms Enter
Sleep 1.5s

# Add the three hosts from ~/.ssh/config.
Type "a" Sleep 1s
Space Sleep 400ms Space Sleep 400ms Space Sleep 600ms
Enter Sleep 2s

# acme-b asks for its sudo password once per session.
Type@40ms "op-sudo-pw" Sleep 300ms Enter
Sleep 8s

# Host detail: Info, then Quorum.
Enter Sleep 2.5s
Tab Sleep 3s
Escape Sleep 1s

# Network upgrades overview.
Type "u" Sleep 4s
Escape Sleep 1s

Type "?" Sleep 2.5s
Escape Sleep 500ms
Type "q"
Sleep 1s
