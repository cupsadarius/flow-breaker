#!/bin/sh
# flow-breaker — Mac alert handler.
#
# Meant to be run by ntfy on the Mac:
#   ntfy subscribe https://<machine>.<tailnet>.ts.net/flow --exec /path/to/alert-handler.sh
#
# ntfy passes the message via the environment: $message, $title, $priority.
# This mirrors the v1 macOS alerts in alert.go — macNotify / macSay / macSound —
# so the Mac keeps speaking once alarms are pushed from the daemon instead of
# fired by the local ticker.

title="${title:-flow-breaker}"
body="${message}"

# osascript takes AppleScript string literals; escape backslashes and quotes so a
# message containing either can't break out of the string, and flatten CR/LF to
# spaces — a raw newline makes AppleScript reject the literal and the banner is
# silently lost.
esc() {
	printf '%s' "$1" | tr '\r\n' '  ' | sed 's/\\/\\\\/g; s/"/\\"/g'
}

# 1. Native notification (banner + Glass sound) — mirrors macNotify.
osascript -e "display notification \"$(esc "$body")\" with title \"$(esc "$title")\" sound name \"Glass\""

# 2. Spoken alert — mirrors macSay.
say -v Samantha -r 200 "$body"

# 3. Alarm sound — mirrors macSound("Funk").
afplay /System/Library/Sounds/Funk.aiff
