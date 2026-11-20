# 无显示器 Debian 的 X11 测试桌面

该示例在独立普通用户 `svnc` 下，通过 Xvfb 创建 1600×900 的 X11 桌面，支持 Xfce 和 KDE Plasma 6。自建 LibVNCServer 后端共享此桌面，Go Agent 提供 SSH 和 SFTP。适用于无显示器测试机；不提供多个登录用户的动态桌面分配。

两种桌面共用 `dbus-x11 xinit xvfb xauth x11-utils xfonts-base`。Debian 13 的 Xfce 方案安装 `xfce4 xfce4-terminal`；可配合 `desktop-base greybird-gtk-theme elementary-xfce-icon-theme fonts-noto-cjk` 提供桌面主题、图标和中文字体。后端构建依赖及凭据准备见 [Linux 后端说明](../../desktop/linux/README.md)。

Plasma 6 可安装 `kde-plasma-desktop kwin-x11 konsole dolphin systemsettings plasma-systemmonitor breeze-gtk-theme libgl1-mesa-dri`，使用 `--no-install-recommends` 时需明确安装 `kwin-x11`。此部署使用 `startplasma-x11`，不启用 Wayland 会话。

布局约定：

| 路径 | 用途 |
| --- | --- |
| `/opt/svnc/bin/sshvnc` | Go Agent |
| `/opt/svnc/backends/linux/svnc-desktop` | 本机编译的 LibVNCServer 后端 |
| `/opt/svnc/state/config.yaml` | 私有配置 |
| `/opt/svnc/state/ssh_host_ed25519_key` | SSH 主机私钥，重启时保留 |
| `/opt/svnc/state/authorized_keys` | 客户端公钥 |
| `/opt/svnc/state/vnc-password.txt` | 独立 VNC 密码 |
| `/home/svnc` | 默认 SFTP 根目录（Agent 运行账户的 HOME） |
| `/run/svnc-x11/Xauthority` | 每次桌面启动时生成的 X11 cookie |
| `/etc/default/svnc-desktop` | 可选桌面选择：`SVNC_DESKTOP=xfce` 或 `SVNC_DESKTOP=plasma` |

使用 `configs/linux.example.yaml`，将 `ssh.listen` 改为 `127.0.0.1:5922`，保留 `sftp.root: "~"`（也可省略），后端可执行文件设为上表绝对路径。二进制由 root 管理；state 由 svnc 所有、目录权限 0700，凭据和配置权限 0600。配置及凭据放在 HOME 之外。svnc 无 sudo 授权。

将两个脚本分别安装为 `/usr/local/libexec/svnc-start-desktop` 和 `/usr/local/libexec/svnc-wait-desktop`，由 root 所有、权限 0755；将两个 service 文件安装到 `/etc/systemd/system/`。准备好普通用户、二进制、配置、凭据后运行：

```sh
systemctl daemon-reload
systemctl enable --now svnc-desktop-session svnc-agent
systemctl status svnc-desktop-session svnc-agent
journalctl -u svnc-desktop-session -u svnc-agent -f
```

脚本默认启动 Xfce；将 `svnc-desktop.example` 安装为 `/etc/default/svnc-desktop` 并修改该变量后可切换。先停止 Agent 和桌面，再启动二者：

```sh
systemctl stop svnc-agent svnc-desktop-session
systemctl start svnc-desktop-session svnc-agent
```

Plasma 使用此服务的私有 D-Bus 会话；可通过 `runuser -u svnc -- kwriteconfig6 --file startkderc --group General --key systemdBoot false` 明确使用经典会话启动，保持桌面进程归该 systemd 服务管理。

`alivnc` 的专用测试账户未设置系统登录密码，Plasma 测试会话已将 `kscreenlockerrc` 的 `Daemon/Autolock` 和 `Daemon/LockOnResume` 设为 false。此设置不改变 Agent 的 SSH 公钥及 VNC 密码认证。

Xvfb 只使用 Unix socket 和私有 Xauthority，VNC 只监听 `127.0.0.1:5900`。保留系统 SSH 作为跳板即可，无需在公网开放 5922/5900。在 SuperVNC Viewer 中使用 Agent `127.0.0.1:5922`、SSH 用户 `vncuser`、桌面连接方式 SVNC，并填写系统 SSH 跳板、Agent 私钥及可信主机公钥。

停止测试环境用 `systemctl stop svnc-agent svnc-desktop-session`；重新启动用 `systemctl start svnc-desktop-session svnc-agent`。重启桌面会终止该虚拟会话中的应用；仅重启 svnc-agent 会中断远程连接并保留桌面应用。

2026-11-20 在 `ssh alivnc` 的 Debian 13 amd64 上部署了此布局。桌面和 Agent 均设为开机启动，系统 SSH 保持原有配置。客户端认证材料位于本机私有目录，不放入仓库。

已验收：SVNC macOS 客户端经 alivnc 跳板显示完整桌面，实际点击打开 Xfce 终端并执行键盘命令；中文文本剪贴板双向传输；SFTP 上传、重命名、下载、内容比对和删除；错误 VNC 密码、None 认证及 RFB 文件传输入口拒绝；不完整握手回收和 Raw/光标/ExtendedClipboard 协商。测试时 Agent SSH RTT 约 15–25ms，此值不是画面或操作端到端延迟。尚未进行断电重启验收或性能基准测试。

同日新增 Plasma 6.3.6 / KWin X11，并将 `/etc/default/svnc-desktop` 设置为 `SVNC_DESKTOP=plasma`。SVNC 连接名称更新为 `alivnc · KDE Plasma 6`。随后 SFTP 根目录由 `/home/svnc/exchange` 改为 HOME `/home/svnc`；旧文件仍在 `exchange` 子目录。已通过客户端打开 Plasma 应用启动器和 Konsole，实际键鼠操作及终端命令正常；切换后尚未重新验收剪贴板和休眠等场景。原 Xfce 包及配置仍保留，修改该变量为 `xfce` 并重启两个服务即可切回。

同日更新 alivnc 的 Go Agent，入口由 `127.0.0.1:2222` 迁为 `127.0.0.1:5922`，支持 VNC subsystem，并保留 SFTP 与原有 `direct-tcpip`。后端仍为 `127.0.0.1:5900`，普通 VNC 兼容入口保持关闭。只重启 `svnc-agent`，Plasma/Xvfb 桌面服务的 PID 保持不变。

通过 subsystem 与旧转发分别完成 1600×900 桌面的真实 RFB 验收：错误密码及 None 认证拒绝、RFB 文件传输拒绝、未完成握手回收、分包协商、Raw 画面、光标和 ExtendedClipboard 能力。5922 上的 SFTP 上传、下载及内容比对通过，测试文件已删除。本机 `alivnc · KDE Plasma 6` 连接已迁为 SVNC subsystem，连接管理页已刷新。此次没有发送远端键鼠输入或改写剪贴板。部署前的程序、配置及 service 记录备份于 `/opt/svnc/backups/before-svnc-5922-20261120-104319`，SSH 主机密钥、授权公钥与 VNC 密码均沿用原配置。
