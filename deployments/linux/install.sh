#!/usr/bin/env bash
# Install the prebuilt JumpVNC Server package for the current X11 desktop user.
set -euo pipefail
umask 077

usage() {
    cat <<'HELP'
JumpVNC Server Linux installer
Usage: bash install.sh [--no-start | --info]
  --no-start  Install now; start automatically at the next X11 desktop login.
  --info      Show this user's current connection information and VNC password.
  --help      Show this help.
Run as the desktop user. The installer requests sudo for system files and dependencies.
HELP
}
mode=install
case "${1:-}" in
    '') ;;
    --no-start) mode=no-start ;;
    --info) mode=info ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
esac
[[ $# -le 1 ]] || { usage >&2; exit 2; }
[[ $(uname -s) == Linux ]] || { echo 'This package requires Linux.' >&2; exit 1; }
[[ $EUID -ne 0 ]] || { echo '请在 X11 桌面终端以普通用户运行 bash install.sh，脚本会按需调用 sudo。' >&2; exit 1; }
account=$(id -un)
prefix=/opt/jumpvnc
state="/var/lib/jumpvnc/$account"
config_home=${XDG_CONFIG_HOME:-$HOME/.config}
if [[ $mode == info ]]; then
    exec "$prefix/bin/jumpvnc" -connection-info -config "$state/config.yaml"
fi
[[ -n ${HOME:-} && $HOME != / ]] || { echo '需要有效的桌面用户主目录。' >&2; exit 1; }
if [[ $mode != no-start ]]; then
    [[ -n ${DISPLAY:-} && ${XDG_SESSION_TYPE:-x11} != wayland ]] || {
        echo '请在已登录的 X11 桌面终端运行；也可使用 --no-start，待下次图形登录启动。' >&2; exit 1;
    }
    systemctl --user show-environment >/dev/null || {
        echo '无法连接 systemd 用户会话；可使用 --no-start 安装，待下次图形登录启动。' >&2; exit 1;
    }
fi
command -v sudo >/dev/null || { echo '需要 sudo 来安装系统目录，请先配置桌面用户的 sudo 权限。' >&2; exit 1; }
command -v systemctl >/dev/null || { echo '需要 systemd 用户服务。' >&2; exit 1; }
source_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
package=$source_dir
if [[ ! -x $package/bin/jumpvnc ]]; then
    package=$(CDPATH= cd -- "$source_dir/../.." && pwd)
fi
for file in bin/jumpvnc backends/linux/svnc-desktop deployments/jumpvnc.service deployments/linux/start-user-session.sh; do
    [[ -f $package/$file ]] || { echo "下载包缺少文件：$file" >&2; exit 1; }
done
"$package/bin/jumpvnc" -help >/dev/null 2>&1 || { echo '程序无法执行，请下载与当前系统架构匹配的包。' >&2; exit 1; }

# Only install distribution packages when a required shared library is absent.
check_libraries() {
    local result
    result=$(ldd "$package/backends/linux/svnc-desktop" 2>&1) || return 1
    [[ $result != *'not found'* ]]
}
if ! check_libraries; then
    # Official server packages are built on Debian 13; other distributions must
    # provide ABI-compatible runtime libraries rather than compiling on install.
    distro_id= distro_version=
    if [[ -r /etc/os-release ]]; then
        distro_id=$(. /etc/os-release; printf '%s' "$ID")
        distro_version=$(. /etc/os-release; printf '%s' "$VERSION_ID")
    fi
    if [[ $distro_id != debian || $distro_version != 13* ]]; then
        echo '缺少兼容运行库。当前下载包基于 Debian 13 构建，详见 Linux 安装说明。' >&2
        ldd "$package/backends/linux/svnc-desktop" >&2 || true
        exit 1
    fi
    sudo apt-get update
    sudo apt-get install -y libavcodec61 libavutil59 libswscale8 libjpeg62-turbo zlib1g libx11-6 libxtst6 libxfixes3
    check_libraries || { echo '运行库仍不匹配，安装已停止。' >&2; exit 1; }
fi

sudo -v
# A downloaded package must not become the running program: its directory can
# be removed after installation. Credentials remain outside the SFTP HOME root.
sudo install -d -m 755 "$prefix/bin" "$prefix/backends/linux" /var/lib/jumpvnc
new_state=false
[[ -d $state ]] || new_state=true
sudo install -d -m 700 -o "$account" -g "$(id -gn)" "$state"
legacy_state="/var/lib/sshvnc/$account"
if $new_state && [[ -d $legacy_state && ! -L $legacy_state ]]; then
    cp -a -- "$legacy_state/." "$state/"
fi
if systemctl --user show-environment >/dev/null 2>&1; then
    systemctl --user stop jumpvnc.service 2>/dev/null || true
    if [[ -f $config_home/systemd/user/sshvnc.service ]] && grep -q 'JumpVNC' "$config_home/systemd/user/sshvnc.service"; then
        systemctl --user disable --now sshvnc.service
    fi
fi
for relative in bin/jumpvnc backends/linux/svnc-desktop; do
    sudo install -m 755 "$package/$relative" "$prefix/$relative.new"
    sudo mv -f -- "$prefix/$relative.new" "$prefix/$relative"
done
sudo install -m 755 "$package/deployments/linux/start-user-session.sh" "$prefix/bin/jumpvnc-session"
sudo install -m 755 "${BASH_SOURCE[0]}" "$prefix/install.sh"
"$prefix/bin/jumpvnc" -initialize-service -config "$state/config.yaml"

mkdir -p "$config_home/systemd/user" "$config_home/autostart"
cp -- "$package/deployments/jumpvnc.service" "$config_home/systemd/user/jumpvnc.service"
cat > "$config_home/autostart/jumpvnc.desktop" <<'DESKTOP'
[Desktop Entry]
Type=Application
Name=JumpVNC Server
Comment=Share the current X11 desktop through SSH
Exec=/opt/jumpvnc/bin/jumpvnc-session
Terminal=false
X-GNOME-Autostart-enabled=true
DESKTOP
if [[ $mode != no-start ]]; then
    "$prefix/bin/jumpvnc-session"
    systemctl --user is-active --quiet jumpvnc.service
fi
printf '\nJumpVNC 安装完成。\n'
"$prefix/bin/jumpvnc" -connection-info -config "$state/config.yaml"
printf '\n再次查看连接信息：bash /opt/jumpvnc/install.sh --info\n'
printf '服务管理：systemctl --user status jumpvnc.service\n'
printf '请按访问来源放行 TCP 5922。程序会随 X11 桌面登录自动启动。\n'
