#!/bin/sh
set -eu
for attempt in $(seq 1 150); do
    if /usr/bin/xdpyinfo >/dev/null 2>&1; then exit 0; fi
    sleep .1
done
echo 'X11 display did not become ready' >&2
exit 1
