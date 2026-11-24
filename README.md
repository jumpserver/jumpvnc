# SuperVNC

SuperVNC 是 JumpServer 团队开发的远程桌面工具。服务端 `sshvnc` 共享 Linux X11 或 Windows 当前登录用户的桌面，macOS 上可用 SuperVNC Viewer 连接。

默认只需开放 TCP 5922。服务端用 SSH 公钥认证，桌面通过 `vnc` 子系统传输 RFB，文件通过同一端口走 SFTP。协议细节见 [SVNC 规范](docs/protocol.md)。

## 下载

打开 [GitHub Actions 构建记录](https://github.com/jumpserver-dev/svnc/actions/workflows/build.yml)，在成功构建的 **Artifacts** 中下载。压缩包附带 SHA-256 校验文件。

| 平台 | 产物 |
| --- | --- |
| macOS（Apple Silicon / Intel） | `supervnc-viewer-macos-universal` |
| Linux（amd64 / arm64） | `supervnc-server-linux-amd64` / `supervnc-server-linux-arm64` |
| Windows（amd64） | `supervnc-server-windows-amd64` |

## Linux 快速启动

在已登录的 X11 桌面中打开终端。需要 Go 1.26.9+、OpenSSH 客户端和 TigerVNC 的桌面共享与密码工具（Debian 上是 `tigervnc-scraping-server` 和 `tigervnc-tools`）。首次部署时执行下列命令，把 `/path/to/client.pub` 换成客户端的 SSH 公钥路径。

```bash
mkdir -p bin
go build -o bin/sshvnc ./cmd/server

umask 077
state=/var/lib/sshvnc/$USER
sudo install -d -m 700 -o "$USER" "$state"
cp configs/server.example.yaml "$state/config.yaml"
ssh-keygen -t ed25519 -N '' -f "$state/ssh_host_ed25519_key"
cp /path/to/client.pub "$state/authorized_keys"
vncpasswd "$state/vnc.passwd"

./bin/sshvnc -config "$state/config.yaml"
```

后续启动沿用同一份主机密钥。SFTP 默认访问运行用户的主目录，通过 `sftp.root` 和 `sftp.mode` 调整目录和权限。配置与凭据放在 SFTP 根目录之外。

也可使用 [LibVNCServer + X11 后端](desktop/linux/README.md)。无显示器的机器见 [Xvfb 部署](deployments/linux/README.md)。

## Windows

在已登录用户的交互会话中运行 `sshvnc.exe`。默认后端是 LibVNCServer + DXGI。双击托盘图标可打开管理窗口，查看状态、启停和重启服务、编辑配置或查看错误。构建和计划任务见 [Windows 部署说明](desktop/windows/README.md)，配置见 [Windows 示例](configs/windows.example.yaml)。

## 连接

在 SuperVNC Viewer 中选择 **SVNC**，填服务器地址、端口 `5922` 和 SSH 用户名（示例为 `vncuser`）。首次连接前核对主机密钥，桌面认证时输入 VNC 密码。文件可从右侧 SFTP 面板传输。[Viewer 使用说明](client/README.md)

普通 VNC Viewer 可通过 SSH 本地转发接入。运行下面的命令前，先将已核验的服务端 SSH 主机公钥加入 `known_hosts`。

```bash
ssh -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
    -p 5922 -N -L 127.0.0.1:15900:127.0.0.1:5900 vncuser@server.example.com
```

保持隧道运行，另开终端运行 `vncviewer 127.0.0.1::15900`。也可用 `sftp -P 5922 vncuser@server.example.com` 直接传文件。

开启 `vnc.compatibility.enabled` 可让普通 Viewer 直连独立 VNC 端口（默认 `5923`）。该端口只使用 VNC 密码认证，流量未经 SSH 加密，仅在可信网络或 VPN 内使用。

Guacamole 可将 VNC 目标指向 guacd 可访问的 SSH 本地转发端口，SFTP 则直接连接 Agent 的 SSH 入口。原生 SVNC 适配见 [JumpServer 与 Guacamole 集成约定](docs/protocol.md#73-jumpserver-与-guacamole-集成约定)。

## 开发

```bash
go test -race ./...
go vet ./...
```
