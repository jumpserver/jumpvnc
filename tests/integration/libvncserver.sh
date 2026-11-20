#!/usr/bin/env bash
# Run as an ordinary user in isolated Linux X11/Xvfb, with the native backend and TigerVNC viewer.
set -euo pipefail
agent=$(realpath "${1:?pass the Linux sshvnc binary}")
export SVNC_TEST_LIBVNC_BACKEND
SVNC_TEST_LIBVNC_BACKEND=$(realpath "${2:?pass the Linux svnc-desktop binary}")
exec bash "$(dirname "${BASH_SOURCE[0]}")/tigervnc.sh" "$agent"
