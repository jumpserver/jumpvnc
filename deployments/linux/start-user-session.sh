#!/usr/bin/env bash
# XDG autostart supplies fresh X11 credentials on every desktop login.
set -euo pipefail
[[ -n ${DISPLAY:-} && ${XDG_SESSION_TYPE:-x11} != wayland ]] || exit 0
config_home=${XDG_CONFIG_HOME:-$HOME/.config}
if [[ -f $config_home/systemd/user/sshvnc.service ]] && grep -q 'JumpVNC' "$config_home/systemd/user/sshvnc.service"; then
    systemctl --user disable --now sshvnc.service
fi
systemctl --user daemon-reload
systemctl --user import-environment DISPLAY
if [[ -n ${XAUTHORITY:-} ]]; then
    systemctl --user import-environment XAUTHORITY
else
    systemctl --user unset-environment XAUTHORITY
fi
if [[ -n ${XDG_SESSION_TYPE:-} ]]; then
    systemctl --user import-environment XDG_SESSION_TYPE
else
    systemctl --user unset-environment XDG_SESSION_TYPE
fi
systemctl --user restart jumpvnc.service
