# sshvnc

`sshvnc` 是单桌面实例的 Go Agent。标准 VNC Viewer 通过 SSH 本地转发连接现有 VNC Server，SFTP 客户端通过同一个 SSH 入口操作专用交换目录。

默认 SSH 入口是 `0.0.0.0:2222`。启用桌面权限后，Agent 只接受指向配置中 IPv4 回环地址和端口的 `direct-tcpip` 请求，示例使用 `127.0.0.1:5900`。SSH 账户绑定到一份公钥列表，桌面权限和 SFTP 模式分别配置。Shell、命令执行、PTY、远程端口转发和 Agent 转发均被拒绝。客户端的 `-L` 和 `-D` 在 SSH 服务端使用同一种通道，因此两者受同一目标白名单约束。

## macOS 桌面客户端

MyGo 客户端在同一窗口提供 VNC 桌面与 SFTP 文件侧栏，支持 Finder 拖入上传。见 [客户端构建与使用说明](client/README.md)。

## Linux X11 快速启动

构建需要 Go 1.26.9 或更新版本。在已登录的 X11 桌面用户下运行。安装 OpenSSH 客户端和 TigerVNC 的当前桌面共享后端及密码工具，例如 Debian 的 `tigervnc-scraping-server` 和 `tigervnc-tools`。默认优先查找前台进程 `X0tigervnc`，其次查找 `x0vncserver`。也可通过 `vnc.executable` 指定前台可执行文件。[TigerVNC 参数文档](https://tigervnc.org/doc/x0vncserver.html)

```bash
go build -o bin/sshvnc ./cmd/server
umask 077
mkdir -p state/exchange
cp configs/server.example.yaml state/config.yaml
ssh-keygen -t ed25519 -N '' -f state/ssh_host_ed25519_key
cp /path/to/client.pub state/authorized_keys
vncpasswd state/vnc.passwd
./bin/sshvnc -config state/config.yaml
```

路径相对于 YAML 文件所在目录。将客户端的公钥写入 `authorized_keys`，一行一把。保留 `ssh_host_ed25519_key` 以便重启后维持同一 SSH 主机身份。选择独立的 VNC 密码，并限制后端密码文件和主机私钥的读取权限。

首次连接前，通过可信途径将主机公钥写入客户端 `known_hosts`，条目格式为 `[server.example.com]:2222 ssh-ed25519 <base64>`。启动日志中的 `ssh.ready` 包含公钥指纹。随后在客户端运行：

```bash
ssh -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
    -p 2222 -N -L 127.0.0.1:15900:127.0.0.1:5900 vncuser@server.example.com
```

另开一个终端连接 Viewer，按提示输入 VNC 密码。需要传输文件时连接 SFTP。

```bash
vncviewer 127.0.0.1::15900
sftp -o StrictHostKeyChecking=yes -P 2222 vncuser@server.example.com
```

需要自动恢复时，将 [systemd 用户服务示例](deployments/sshvnc.service) 放入 `~/.config/systemd/user/`，将二进制和配置放到 unit 指定的位置。从 X11 桌面导入环境后运行 `systemctl --user daemon-reload` 和 `systemctl --user start sshvnc`。

```bash
systemctl --user import-environment DISPLAY XAUTHORITY
```

## Windows 当前用户桌面

```powershell
go build -o bin/sshvnc.exe ./cmd/server
```

1. 复制 [Windows 配置示例](configs/windows.example.yaml)，准备相同的 SSH 主机密钥、公钥列表和专用交换目录。使用 NTFS ACL 将 Agent 配置、密钥、后端配置和目录限定给运行账户及管理员。
2. 将待验证版本的 UltraVNC `winvnc.exe` 和配套依赖放入配置指定的后端目录。在同目录创建 `ultravnc.portable`，根据 [INI 示例](deployments/ultravnc.ini.example) 配置 `ultravnc.ini`，使用 UltraVNC 自带的密码设置工具生成 `[UltraVNC]` 下的 `passwd`。示例用 `RemoveWallpaper=0` 保留 VNC 会话中的 Windows 壁纸。
3. 在当前登录用户会话执行 `bin\sshvnc.exe -config state\config.yaml`。Agent 校验 INI 中的回环、认证和端口设置，再以普通子进程方式启动 `winvnc.exe -run`。[UltraVNC 命令行](https://uvnc.com/docs/ultravnc-server/51-ultravnc-server-commandline-parameters.html) · [INI 字段](https://uvnc.com/docs/ultravnc-server/69-ultravnc-ini.html)
4. 在目标 Windows 版本使用 `Get-NetTCPConnection -State Listen -LocalPort 5900` 确认只监听 `127.0.0.1`。通过 Windows OpenSSH 登录执行的进程通常位于 Session 0。若要共享当前用户桌面，请在已登录用户的交互会话中启动 Agent。

已在 Windows 10 22H2 x64 的交互控制台会话验证 UltraVNC 1.8.3.2 ZIP 版。通过 OpenSSH 隧道完成 VNC 密码认证、画面读取和测试窗口键鼠输入，并验证了回环监听、正常退出、后端异常退出和 SSH 主机密钥持久化。Windows SFTP 已验证中文文件名、上传下载、目录边界、硬链接拒绝和大小上限。测试使用管理员账户，标准用户权限、中文剪贴板和 Process Monitor 细粒度审计仍待验证。[UltraVNC 1.8.3.2 官方下载](https://uvnc.com/downloads/ultravnc/175-ultravnc-1-8-3-2.html)

## 配置与运行行为

- `ssh.allow_vnc` 控制桌面授权。启用时，Agent 等待 RFB 握手标识后开始接收 SSH 会话。托管后端退出会触发 Agent 退出，由进程管理器恢复。使用 `vnc.engine: external` 时，由独立服务负责后端启停、仅回环监听和独立 VNC 认证。
- `vnc.view_only` 为托管后端启用只读桌面。配置 UltraVNC 时同步设置 `InputsEnabled=0`。文本剪贴板走 VNC，中文兼容性取决于后端和 Viewer。文件传输走独立 SFTP。
- `sftp.mode` 支持 `disabled`、`read_only`、`write_only` 和 `read_write`。`write_only` 可上传、检查指定路径的元数据和管理目录中的文件，禁止下载与目录枚举。服务支持上传、下载、列目录、重命名、删除和创建目录。文件与目录以受限权限创建，禁止创建链接、变更属主和保留客户端文件属性。
- SFTP 使用 Go 的 `os.Root` 将操作限制在交换目录，打开文件时核验类型与硬链接计数。以非特权桌面账户运行，交换目录由该账户专用，勿暴露整个家目录或在交换目录引入其他用户可写的挂载。单文件上传大小由 `max_file_size_mib` 限制。目录总容量上限由部署侧通过文件系统配额或专用卷配置。
- SSH 限制同源 IP 的握手频率、认证尝试、连接和通道数量，并设置握手与空闲超时。SFTP 另有限制每个会话的打开句柄数。审计以 JSON 写到 stderr，通过 `connection_id` 和 `channel_id` 关联认证、转发、会话与文件操作。设置 `audit: false` 可关闭访问审计。

Go Agent 与 VNC 后端分别安装。本项目提供 Linux X11 和当前登录 Windows 用户桌面的配置。Wayland、Windows 安全桌面、多租户桌面分配和跨 Viewer 剪贴板兼容性仍需后续实现或验收。

## JumpServer 与 guacd 接线

代理为获授权的会话建立 SSH `-N -L`，在 guacd 可访问的同一网络命名空间分配一个本地回环端口。将 Guacamole VNC 的 `hostname` 和 `port` 指向这个端口，并提供独立的 VNC 密码。会话关闭或授权收回时，代理需关闭隧道。

在同一 Guacamole 连接中使用 `enable-sftp: true`，将 `sftp-hostname`、`sftp-port` 直接指向 Agent 的 SSH 入口。使用 `sftp-username`、`sftp-private-key` 和 `sftp-host-key` 配置凭据与主机校验，`sftp-root-directory` 可设为 `/`。JumpServer 的资产授权、隧道生命周期、录像和文件 UI 需在目标部署中接入并验收。[Guacamole 官方配置文档](https://guacamole.apache.org/doc/gug/configuring-guacamole.html)

## 验证

```bash
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sshvnc.exe ./cmd/server
```

在有 TigerVNC、OpenSSH 客户端、Xvfb、xterm、xdotool 和 `ss` 的隔离 Linux 环境，以非特权账户运行完整桌面测试：

```bash
go build -o bin/sshvnc ./cmd/server
tests/integration/tigervnc.sh bin/sshvnc
```
