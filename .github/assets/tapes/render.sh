#!/usr/bin/env bash
# Renders the README screenshots and the hero GIF into .github/assets by
# driving the real CLI and TUI against live labs with the tapes next to this
# script. It must run inside a private network namespace, so the lab ports
# never collide with anything else on the machine. From the repo root, in a
# shell with Go (devenv shell), fetch vhs and gifsicle outside the namespace
# and run the script inside it:
#
#   nix shell nixpkgs#vhs nixpkgs#gifsicle -c unshare -rn sh -c 'ip link set lo up && .github/assets/tapes/render.sh'
#
# The namespace has no network, so the chain binaries come from local files:
#
#   FORKLAB_SIMD        simd v0.53.8
#   FORKLAB_EXRPD_OLD   exrpd v11.1.1
#   FORKLAB_EXRPD_NEW   exrpd v11.2.0
set -euo pipefail

: "${FORKLAB_SIMD:?set FORKLAB_SIMD to a simd v0.53.8 binary}"
: "${FORKLAB_EXRPD_OLD:?set FORKLAB_EXRPD_OLD to an exrpd v11.1.1 binary}"
: "${FORKLAB_EXRPD_NEW:?set FORKLAB_EXRPD_NEW to an exrpd v11.2.0 binary}"

assets=$(cd "$(dirname "$0")/.." && pwd)
repo=$(cd "$assets/../.." && pwd)
# A fixed short work dir keeps unix socket paths under 107 bytes and keeps
# random paths out of the images.
work=/tmp/forklab
chmod -R u+w "$work" 2>/dev/null || true
rm -rf "$work"
mkdir -p "$work"

export FORKLAB_HOME=$work/home
export FORKLAB_CONFIG_DIR=$work/config
export FORKLAB_THEME=tokyonight
export PATH=$work/bin:$PATH
# Chromium cannot build its sandbox inside the user namespace.
export VHS_NO_SANDBOX=true

cleanup() {
	for lab in demo xrp; do forklab lab down "$lab" >/dev/null 2>&1 || true; done
	pkill -f "$work/" 2>/dev/null || true
	chmod -R u+w "$work" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

# wait_for runs a command every second until its output matches a pattern.
wait_for() {
	local pattern=$1 timeout=$2
	shift 2
	for _ in $(seq "$timeout"); do
		if "$@" 2>/dev/null | grep -q "$pattern"; then return 0; fi
		sleep 1
	done
	echo "timed out waiting for '$pattern' from: $*" >&2
	return 1
}

step() { printf '\n==> %s\n' "$*" >&2; }

step "building forklab"
(cd "$repo" && go build -o "$work/bin/forklab" ./cmd/forklab)

step "pointing the simd and xrplevm profiles at local binaries"
# Short links keep the binary paths the TUI shows readable.
mkdir -p "$work/chains"
ln -s "$(realpath "$FORKLAB_SIMD")" "$work/chains/simd-0.53.8"
ln -s "$(realpath "$FORKLAB_EXRPD_OLD")" "$work/chains/exrpd-11.1.1"
ln -s "$(realpath "$FORKLAB_EXRPD_NEW")" "$work/chains/exrpd-11.2.0"
forklab profile edit simd --binary "0.53.8=path:$work/chains/simd-0.53.8" >/dev/null
forklab profile edit xrplevm \
	--binary "11.1.1=path:$work/chains/exrpd-11.1.1" \
	--binary "11.2.0=path:$work/chains/exrpd-11.2.0" >/dev/null

cd "$assets"

step "wizard.tape: the first-run wizard, while no lab exists"
if ! vhs -q tapes/wizard.tape >/dev/null 2>&1; then
	echo "skipped wizard.png: this forklab has no first-run wizard" >&2
	rm -f wizard.png
fi

step "cli.tape: create and start lab demo"
vhs -q tapes/cli.tape

step "filling lab demo with transfers and proposals"
forklab account send test0 test1 250000 >/dev/null
forklab account send test2 test3 1000 >/dev/null
forklab gov submit --template text --title "Community pool spend for relayers" --auto-vote >/dev/null
wait_for PASSED 90 forklab gov list
forklab gov submit --template text --title "Lower the minimum deposit" --auto-vote >/dev/null

step "tui.tape and hero.tape"
vhs -q tapes/tui.tape
vhs -q tapes/hero.tape
forklab lab down demo >/dev/null

step "upgrade.tape: schedule xrplevm 11.1.1 -> 11.2.0 from the TUI form"
forklab lab create xrp --profile xrplevm --version 11.1.1 --validators 2 --chain-id xrplevm_1449999-1 >/dev/null
forklab lab up xrp >/dev/null
vhs -q tapes/upgrade.tape
wait_for swapped 120 forklab upgrade status
forklab lab down xrp >/dev/null

step "optimizing the hero GIF"
gifsicle -O3 --lossy=80 --colors 64 -o hero.gif hero.gif
rm -f cli.gif tui.gif upgrade.gif wizard.gif
ls -l "$assets"/*.png "$assets"/*.gif
