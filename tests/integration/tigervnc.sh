#!/usr/bin/env bash
# Run in an isolated Linux test environment with TigerVNC, Xvfb, xterm, xdotool,
# OpenSSH client tools and ss. Use an ordinary desktop user and free TCP ports.
set -euo pipefail
umask 077

agent=$(realpath "${1:?pass the Linux sshvnc binary}")
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
tmp=$(mktemp -d)
pids=()
cleanup() {
    local status=$?
    if (( status != 0 )); then
        for log in "$tmp"/*.log; do
            [[ -f $log ]] && { printf '\n%s\n' "$log"; cat "$log"; }
        done
    fi
    if (( ${#pids[@]} )); then
        kill "${pids[@]}" 2>/dev/null || true
        wait "${pids[@]}" 2>/dev/null || true
    fi
    rm -rf "$tmp"
}
trap cleanup EXIT

# Wait up to ten seconds for a condition with visible process failures in the logs.
until_ready() {
    for ((attempt = 0; attempt < 100; attempt++)); do
        if "$@"; then return; fi
        sleep .1
    done
    return 1
}

Xvfb -displayfd 3 -nolisten tcp -screen 0 "${SVNC_TEST_DESKTOP_GEOMETRY:-800x600x24}" 3>"$tmp/desktop" >"$tmp/desktop.log" 2>&1 &
pids+=("$!")
Xvfb -displayfd 3 -nolisten tcp -screen 0 1024x768x24 3>"$tmp/viewer" >"$tmp/viewer-x.log" 2>&1 &
pids+=("$!")
until_ready test -s "$tmp/desktop"
until_ready test -s "$tmp/viewer"
desktop=:"$(cat "$tmp/desktop")"
viewer=:"$(cat "$tmp/viewer")"

mkdir "$tmp/exchange"
ssh-keygen -q -t ed25519 -N '' -f "$tmp/ssh_host_ed25519_key"
ssh-keygen -q -t ed25519 -N '' -f "$tmp/client"
cp "$tmp/client.pub" "$tmp/authorized_keys"
printf '[127.0.0.1]:5922 %s\n' "$(cut -d ' ' -f 1,2 "$tmp/ssh_host_ed25519_key.pub")" > "$tmp/hosts"
# The viewer uses vncpasswd format; the native LibVNCServer backend uses a private plaintext file.
printf 'svnctest\n' | vncpasswd -f > "$tmp/viewer.passwd"
if [[ -n ${SVNC_TEST_LIBVNC_BACKEND:-} ]]; then
    mkdir -p "$tmp/backends/linux"
    cp "$SVNC_TEST_LIBVNC_BACKEND" "$tmp/backends/linux/svnc-desktop"
    chmod 700 "$tmp/backends/linux/svnc-desktop"
    printf 'svnctest\n' > "$tmp/vnc-password.txt"
    cp "$repo/configs/linux.example.yaml" "$tmp/config.yaml"
else
    cp "$tmp/viewer.passwd" "$tmp/vnc.passwd"
    cp "$repo/configs/server.example.yaml" "$tmp/config.yaml"
fi

# Keep integration fixtures isolated from the test user's real HOME.
sed -i 's|root: "~".*|root: exchange|' "$tmp/config.yaml"

DISPLAY=$desktop "$agent" -config "$tmp/config.yaml" >"$tmp/agent.log" 2>&1 &
agent_pid=$!
pids+=("$agent_pid")
until_ready grep -qs '"msg":"ssh.ready"' "$tmp/agent.log"
[[ $(ss -H -ltn 'sport = :5900' | awk '{print $4}') == '127.0.0.1:5900' ]]

# Verify direct loopback RFB requires VNC authentication even without SSH.
(
    exec 3<>/dev/tcp/127.0.0.1/5900
    IFS= read -r -t 3 -u 3 version
    [[ $version == 'RFB 003.008' ]]
    printf 'RFB 003.008\n' >&3
    [[ $(timeout 3 od -An -N 2 -t u1 <&3 | xargs) == '1 2' ]]
)

options=(-o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes
         -o UserKnownHostsFile="$tmp/hosts" -i "$tmp/client")
ssh -p 5922 "${options[@]}" -N -L 127.0.0.1:15900:127.0.0.1:5900 \
    -o ExitOnForwardFailure=yes vncuser@127.0.0.1 >"$tmp/tunnel.log" 2>&1 &
pids+=("$!")
until_ready sh -c 'ss -H -ltn "sport = :15900" | grep -q 15900'

if ssh -p 5922 "${options[@]}" -W localhost:5900 vncuser@127.0.0.1 </dev/null >"$tmp/denied.log" 2>&1; then
    exit 1
fi
grep -q 'forwarding target not permitted' "$tmp/denied.log"
if ssh -p 5922 "${options[@]}" vncuser@127.0.0.1 true >"$tmp/exec.log" 2>&1; then
    exit 1
fi
grep -q 'exec request failed' "$tmp/exec.log"

DISPLAY=$desktop xterm -geometry 64x16+40+40 -T sshvnc-input -e sh -c \
    'IFS= read -r line; printf "%s\n" "$line" > "$1"' sh "$tmp/remote-input" >"$tmp/xterm.log" 2>&1 &
pids+=("$!")
until_ready env DISPLAY="$desktop" xdotool search --onlyvisible --name sshvnc-input >/dev/null
DISPLAY=$viewer vncviewer -Shared -PasswordFile "$tmp/viewer.passwd" \
    127.0.0.1::15900 >"$tmp/viewer.log" 2>&1 &
pids+=("$!")
until_ready grep -qsi 'Using pixel format' "$tmp/viewer.log"
viewer_window=$(DISPLAY=$viewer xdotool search --maxdepth 1 --onlyvisible --name '.+' | tail -1)
desktop_window=$(DISPLAY=$desktop xdotool search --onlyvisible --name sshvnc-input | tail -1)
# Xvfb has no window manager to assign keyboard focus.
DISPLAY=$desktop xdotool windowfocus "$desktop_window"
DISPLAY=$viewer xdotool windowfocus "$viewer_window"
DISPLAY=$viewer xdotool mousemove --window "$viewer_window" 110 110 click 1
until_ready sh -c 'DISPLAY=$1 xdotool getmouselocation --shell | grep -qx X=110' sh "$desktop"
DISPLAY=$viewer xdotool type 'through-ssh-vnc'
DISPLAY=$viewer xdotool key Return
until_ready grep -qs '^through-ssh-vnc$' "$tmp/remote-input"

if [[ -n ${SVNC_TEST_LIBVNC_BACKEND:-} ]]; then
    # Exercise actual ExtendedClipboard through the independent TigerVNC viewer.
    # xclip owners are bounded by the temporary X servers and also reaped on exit.
    printf 'viewer → Linux 中文\n' > "$tmp/clipboard-in"
    DISPLAY=$viewer xclip -quiet -selection clipboard -target UTF8_STRING -in "$tmp/clipboard-in" >"$tmp/clipboard-viewer.log" 2>&1 &
    pids+=("$!")
    until_ready sh -c 'timeout 1 env DISPLAY="$1" xclip -selection clipboard -target UTF8_STRING -out > "$2" 2>/dev/null && cmp -s "$2" "$3"' \
        sh "$desktop" "$tmp/clipboard-out" "$tmp/clipboard-in"
    printf 'Linux → viewer 中文\n' > "$tmp/clipboard-back"
    DISPLAY=$desktop xclip -quiet -selection clipboard -target UTF8_STRING -in "$tmp/clipboard-back" >"$tmp/clipboard-desktop.log" 2>&1 &
    pids+=("$!")
    until_ready sh -c 'timeout 1 env DISPLAY="$1" xclip -selection clipboard -target UTF8_STRING -out > "$2" 2>/dev/null && cmp -s "$2" "$3"' \
        sh "$viewer" "$tmp/clipboard-result" "$tmp/clipboard-back"
    if [[ -n ${SVNC_RFB_TEST_BINARY:-} ]]; then
        SVNC_RFB_ADDRESS=127.0.0.1:15900 SVNC_RFB_PASSWORD_FILE="$tmp/vnc-password.txt" \
            "$SVNC_RFB_TEST_BINARY" -test.run '^TestLibVNCBackend$' -test.v
        if [[ ${SVNC_TEST_H264:-0} == 1 ]]; then
            SVNC_TEST_H264_DECODE=1 SVNC_RFB_H264_ADDRESS=127.0.0.1:15900 SVNC_RFB_PASSWORD_FILE="$tmp/vnc-password.txt" \
                "$SVNC_RFB_TEST_BINARY" -test.run '^TestLibVNCH264' -test.v
        fi
        if [[ ${SVNC_TEST_H264_ODD_GEOMETRY:-0} == 1 ]]; then
            SVNC_RFB_H264_ADDRESS=127.0.0.1:15900 SVNC_RFB_PASSWORD_FILE="$tmp/vnc-password.txt" \
                "$SVNC_RFB_TEST_BINARY" -test.run '^TestLibVNCH264UnsupportedGeometry$' -test.v
        fi
    fi
fi

printf 'SFTP fixture\n' > "$tmp/upload"
printf 'put "%s" /sample\nrename /sample /renamed\nget /renamed "%s"\nrm /renamed\n' \
    "$tmp/upload" "$tmp/download" > "$tmp/batch"
sftp -b "$tmp/batch" -P 5922 "${options[@]}" vncuser@127.0.0.1
cmp "$tmp/upload" "$tmp/download"

kill -TERM "$agent_pid"
until_ready sh -c '! kill -0 "$1" 2>/dev/null' sh "$agent_pid"
wait "$agent_pid"
[[ -z $(ss -H -ltn 'sport = :5900') ]]
grep -q '"msg":"vnc.session".*"event":"end"' "$tmp/agent.log"
grep -q '"msg":"sftp.operation".*"bytes_written":13' "$tmp/agent.log"

DISPLAY=$desktop "$agent" -config "$tmp/config.yaml" >"$tmp/restart.log" 2>&1 &
agent_pid=$!
pids+=("$agent_pid")
until_ready grep -qs '"msg":"ssh.ready"' "$tmp/restart.log"
printf 'pwd\n' | sftp -b - -P 5922 "${options[@]}" vncuser@127.0.0.1
kill -TERM "$agent_pid"
wait "$agent_pid"
[[ -z $(ss -H -ltn 'sport = :5900') ]]
echo 'OpenSSH, VNC keyboard and pointer, OpenSSH SFTP, shutdown, and host key persistence passed.'
