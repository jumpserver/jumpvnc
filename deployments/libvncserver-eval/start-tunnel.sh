#!/bin/sh
set -eu
state_dir="$HOME/.ssh/sshvnc-g15"
log_dir="$HOME/Library/Logs/sshvnc-libvnc"
mkdir -p "$log_dir"
launchctl submit -l com.sshvnc.libvncserver-test -o "$log_dir/tunnel.log" -e "$log_dir/tunnel-error.log" -- /usr/bin/ssh -N -T -J g15 -p 2223 -i "$state_dir/client" -o "UserKnownHostsFile=$state_dir/known_hosts" -o HostKeyAlias=sshvnc-g15 -o StrictHostKeyChecking=yes -o BatchMode=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -L 127.0.0.1:15903:127.0.0.1:5902 vncuser@127.0.0.1
