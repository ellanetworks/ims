#!/usr/bin/env bash
# The PTT's timings on a phone, over adb: Ella MCPTT registered and in the foreground, its group with another member
# affiliated. Of a press with no call, one in a call and ROUNDS right after a Leave: the access time (the floor
# granted) and the first speech frame after the grant; then that no call's audio is left running once all have ended.
# Fails when a first frame comes after FIRST_FRAME_MS, or audio is left.
set -euo pipefail

dev=${1:?usage: ptt-timing.sh <adb serial> [rounds]}
rounds=${2:-5}
limit=${FIRST_FRAME_MS:-1000}
failed=0

a() { timeout 15 adb -s "$dev" "$@"; }

# The centre of the node whose text or content description is $1, if shown.
centre() {
	a exec-out uiautomator dump /dev/tty 2>/dev/null |
		grep -o "<node[^>]*\"$1\"[^>]*>" | head -1 |
		sed -nE 's/.*bounds="\[([0-9]+),([0-9]+)\]\[([0-9]+),([0-9]+)\]".*/\1 \2 \3 \4/p' |
		awk '{ print int(($1 + $3) / 2), int(($2 + $4) / 2) }' || true
}

now() { a shell "date +'%m-%d %H:%M:%S.%3N'"; }

# Holds the PTT for $1 ms.
press() {
	read -r x y <<<"$(centre 'Push to talk')"
	a shell input swipe "$x" "$y" "$x" "$y" "$1"
}

leave() {
	local at
	at=$(centre Leave)
	[ -n "$at" ] || return 0
	read -r x y <<<"$at"
	a shell input tap "$x" "$y"
}

# Milliseconds of a Go duration (130.5ms, 1.2s, 900µs).
ms() {
	awk -v d="$1" 'BEGIN {
		if (d ~ /ms$/) print int(substr(d, 1, length(d) - 2));
		else if (d ~ /µs$/) print 0;
		else if (d ~ /s$/) print int(substr(d, 1, length(d) - 1) * 1000);
	}'
}

# The access time and first frame of the press since $2, as row $1.
row() {
	local log access frame
	log=$(a logcat -d -v time -T "$2" -s EllaMcptt:I)
	access=$(grep -o 'msg="floor granted" access=[^ ]*' <<<"$log" | tail -1 | sed 's/.*access=//')
	frame=$(grep -o 'msg="first frame" after=[^ ]*' <<<"$log" | tail -1 | sed 's/.*after=//')
	access=$([ -n "$access" ] && ms "$access" || echo -)
	frame=$([ -n "$frame" ] && ms "$frame" || echo -)
	printf '%-12s %8s %12s\n' "$1" "$access" "$frame"
	if [ "$frame" = - ] || [ "$frame" -gt "$limit" ]; then failed=1; fi
}

a shell input keyevent KEYCODE_WAKEUP
leave
sleep 3

printf '%-12s %8s %12s\n' press 'access' 'first frame'
t=$(now); press 2500; sleep 1; row 'new call' "$t"
t=$(now); press 2000; sleep 1; row 'in call' "$t"
for i in $(seq "$rounds"); do
	leave
	sleep 1
	t=$(now); press 2500; sleep 1; row "after leave" "$t"
done
leave
sleep 3

pid=$(a shell pidof com.ellanetworks.mcptt)
left=$(a shell ps -T -p "$pid" | awk '$NF == "capture" || $NF == "playout"' | wc -l)
echo "audio threads left: $left"
[ "$left" -eq 0 ] || failed=1

exit "$failed"
