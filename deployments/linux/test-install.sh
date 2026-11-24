#!/usr/bin/env bash
# Acceptance on a disposable Linux build runner; never run on a production host.
set -euo pipefail
umask 077
package=$(realpath "${1:?pass the extracted Linux server package}")
account=svnc-ci
home=$(getent passwd "$account" | cut -d: -f6)
state=/var/lib/jumpvnc/$account
sudoers=/etc/sudoers.d/jumpvnc-installer-ci
trap 'rm -f "$sudoers"' EXIT
printf '%s ALL=(root) NOPASSWD: ALL\n' "$account" > "$sudoers"
chmod 440 "$sudoers"
# Install the exact tar contents, using the real Go initializer and ELF backend.
runuser -u "$account" -- bash "$package/install.sh" --no-start > /tmp/jumpvnc-install-output
for file in config.yaml ssh_host_ed25519_key client_ed25519_key authorized_keys vnc-password.txt; do
    test "$(stat -c %a "$state/$file")" = 600
    test "$(stat -c %U "$state/$file")" = "$account"
done
test -f "$home/.config/autostart/jumpvnc.desktop"
test -f "$home/.config/systemd/user/jumpvnc.service"
test -x /opt/jumpvnc/bin/jumpvnc-session
# Customized access lists must survive upgrades, including an intentional revoke.
printf '# installed key revoked by the administrator\n' > "$state/authorized_keys"
sha256sum "$state/config.yaml" "$state/ssh_host_ed25519_key" "$state/client_ed25519_key" "$state/authorized_keys" "$state/vnc-password.txt" > /tmp/jumpvnc-install-before.sha256
runuser -u "$account" -- bash "$package/install.sh" --no-start > /tmp/jumpvnc-upgrade-output
sha256sum -c /tmp/jumpvnc-install-before.sha256
# Simulate the old state directory and verify migration keeps all credentials.
mkdir -p /var/lib/sshvnc
mv "$state" "/var/lib/sshvnc/$account"
runuser -u "$account" -- bash "$package/install.sh" --no-start > /tmp/jumpvnc-migrate-output
sha256sum -c /tmp/jumpvnc-install-before.sha256
runuser -u "$account" -- bash /opt/jumpvnc/install.sh --info > /tmp/jumpvnc-info-output
grep -q '使用原有客户端私钥' /tmp/jumpvnc-info-output
if grep -q 'BEGIN OPENSSH PRIVATE KEY' /tmp/jumpvnc-info-output; then
    echo 'Connection information leaked a private key' >&2
    exit 1
fi
printf 'PASS: Linux package installation, private credentials, autostart, upgrade preservation and connection information\n'
