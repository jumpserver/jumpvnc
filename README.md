# SuperVNC

SuperVNC 是由 JumpServer 团队开发的远程桌面产品，也是 **SVNC 接入协议**的参考实现。产品名使用 SuperVNC，客户端和服务端分别称为 SuperVNC Viewer、SuperVNC Server；协议名保留 SVNC，接入类型标识使用 `svnc`，与普通 VNC 的 `vnc` 区分。

SVNC 的标准化对象是 SSH 单端口入口、桌面子系统、可选文件通道及其授权与兼容规则；桌面数据继续使用 RFB，文件传输继续使用 SFTP。协议允许第三方独立实现，不要求依赖 JumpServer 或 SuperVNC 的界面与桌面后端。

客户端应用构建为 `SuperVNC.app`。服务端 Go Agent 的可执行文件仍为 `sshvnc`，桌面后端仍为 `svnc-desktop`；仓库目录 `svnc`、已有部署路径、服务名和客户端配置及凭据标识继续沿用，避免品牌调整影响已有部署与连接。

协议定义与互通约定见 [SVNC 接入协议规范（v0.1 草案）](docs/protocol.md)，涵盖 SSH 子系统、RFB 数据流、可选 SFTP、权限边界与扩展规则。

SuperVNC Server 的 `sshvnc` 是单桌面实例的 Go Agent。内置客户端通过 SSH 的 `vnc` subsystem 连接桌面，标准 VNC Viewer 和 Guacamole 也可通过 SSH 本地转发连接同一桌面后端，Windows 默认使用自建 LibVNCServer/DXGI 后端，SFTP 客户端通过同一个 SSH 入口访问运行账户的 HOME（也可配置其他根目录）。

默认 SVNC/SSH 入口是 `0.0.0.0:5922`，同时支持 `vnc` subsystem、`sftp` subsystem 和原有 `direct-tcpip` 转发。启用桌面权限后，`vnc` subsystem 自动连接配置的桌面后端；`direct-tcpip` 仍只接受指向配置中 IPv4 回环地址和端口的请求，示例使用 `127.0.0.1:5900`。SSH 账户绑定到一份公钥列表，桌面权限和 SFTP 模式分别配置。Shell、命令执行、PTY、远程端口转发和 Agent 转发均被拒绝。客户端的 `-L` 和 `-D` 在 SSH 服务端使用同一种通道，因此两者受同一目标白名单约束。

## macOS 桌面客户端

SuperVNC Viewer 使用 MyGo，在同一窗口提供 VNC 桌面与 SFTP 文件侧栏，支持 Finder 拖入上传。见 [客户端构建与使用说明](client/README.md)。

## 自动构建与下载

[Build SuperVNC](https://github.com/jumpserver-dev/svnc/actions/workflows/build.yml) 在 push、pull request 或手动运行时执行测试并生成以下 Actions artifacts，保留 14 天：

| 产物 | 内容 |
| --- | --- |
| `supervnc-server-linux-amd64` / `supervnc-server-linux-arm64` | Go Agent、LibVNCServer/X11 后端、配置示例、部署脚本和对应源码 |
| `supervnc-server-windows-amd64` | Go Agent、LibVNCServer/DXGI 后端、配置示例、计划任务脚本、许可及对应源码 |
| `supervnc-viewer-macos-universal` | 同时支持 Apple Silicon 和 Intel 的 `SuperVNC.app` 及对应源码 |

打开成功的运行记录，在 **Artifacts** 下载压缩包；每个包附带 SHA-256 校验文件。Linux 构建使用 Debian 13，目标系统需提供相容的 glibc、LibVNCServer 0.9.15+ 和 X11 运行库。Windows 需要 x64 Visual C++ 运行库。macOS 应用使用 ad hoc 签名，尚未做 Developer ID 签名或公证。流程不创建 Release，也不部署到远端机器。

Linux CI 在隔离的 Xvfb 桌面中分别验证原生后端和 TigerVNC 的桌面、键鼠、剪贴板及 SFTP；Windows 验证 Go 测试及后端编译，macOS 验证客户端/前端测试、双架构与应用签名。真实 Windows 桌面的键鼠和剪贴板仍需实机验收。

## 单端口与普通 VNC Viewer

默认只需开放 TCP 5922：SSH 负责加密与认证，`vnc` subsystem 提供标准 RFB 数据流，`sftp` subsystem 提供文件传输。两者可在同一 SSH 连接中使用，桌面权限由 `ssh.allow_vnc` 控制，SFTP 权限由 `sftp.mode` 独立控制。关闭 SFTP 后仍能使用桌面。

内置客户端默认选择 SVNC，无需填写服务端后端的 VNC 端口。已有连接保留原有接入方式；连接旧 Agent 时可显式选择 VNC 并启用「使用 SSH 隧道」。现有 SSH `-L`、Guacamole 和 SFTP 接入保持兼容。

普通 Viewer 可使用下文的 SSH 隧道，或显式启用独立的 VNC 兼容入口。在已有 `vnc` 配置下添加：

```yaml
vnc:
  # 保留已有 engine、listen、password_file 等配置。
  compatibility:
    enabled: true
    # listen: "0.0.0.0:5923"
```

省略 `compatibility.listen` 时，兼容入口使用 `ssh.listen` 相同的监听地址、端口加一；默认即 `0.0.0.0:5923`。也可显式指定其他地址与端口。后端继续只监听回环地址，Agent 将兼容入口的原始 RFB 数据转给同一后端；不会自动识别 5922 上的裸 VNC 请求。

例如 TigerVNC Viewer 使用 `vncviewer server.example.com::5923`（其他 Viewer 按其地址格式填写主机与端口）。此入口使用后端原有的 VNC 密码认证，不经过 SSH 认证或加密；只在可信网络或已有 VPN/加密隧道中启用。使用外部后端时，部署者必须确保后端启用了 VNC 认证。兼容入口使用与 SSH 入口相同的连接数、每 IP 接入频率和空闲超时设置，配额分别计算。

## Linux X11 快速启动

Linux 也可使用自建 **LibVNCServer + X11** 后端，通过 `vnc.engine: libvncserver` 启用。构建、部署、支持范围见 [Linux 后端说明](desktop/linux/README.md)，配置见 [linux.example.yaml](configs/linux.example.yaml)。以下为原有 TigerVNC 后端的启动方式。

构建需要 Go 1.26.9 或更新版本。在已登录的 X11 桌面用户下运行。安装 OpenSSH 客户端和 TigerVNC 的当前桌面共享后端及密码工具，例如 Debian 的 `tigervnc-scraping-server` 和 `tigervnc-tools`。默认优先查找前台进程 `X0tigervnc`，其次查找 `x0vncserver`。也可通过 `vnc.executable` 指定前台可执行文件。[TigerVNC 参数文档](https://tigervnc.org/doc/x0vncserver.html)

```bash
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

路径相对于 YAML 文件所在目录。将客户端的公钥写入 `authorized_keys`，一行一把。保留 `ssh_host_ed25519_key` 以便重启后维持同一 SSH 主机身份。选择独立的 VNC 密码，并限制后端密码文件和主机私钥的读取权限。

首次连接前，通过可信途径将主机公钥写入客户端 `known_hosts`，条目格式为 `[server.example.com]:5922 ssh-ed25519 <base64>`。启动日志中的 `ssh.ready` 包含公钥指纹。随后在客户端运行：

```bash
ssh -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
    -p 5922 -N -L 127.0.0.1:15900:127.0.0.1:5900 vncuser@server.example.com
```

另开一个终端连接 Viewer，按提示输入 VNC 密码。需要传输文件时连接 SFTP。

```bash
vncviewer 127.0.0.1::15900
sftp -o StrictHostKeyChecking=yes -P 5922 vncuser@server.example.com
```

需要自动恢复时，将 [systemd 用户服务示例](deployments/sshvnc.service) 放入 `~/.config/systemd/user/`，将二进制和配置放到 unit 指定的位置。从 X11 桌面导入环境后运行 `systemctl --user daemon-reload` 和 `systemctl --user start sshvnc`。

```bash
systemctl --user import-environment DISPLAY XAUTHORITY
```

## Windows 当前用户桌面

Windows 默认后端为自建的 **LibVNCServer + DXGI** 桌面程序。构建、部署、计划任务、文本剪贴板和已知限制见 [Windows 后端说明](desktop/windows/README.md)。Go Agent、桌面后端和 SFTP 使用同一部署，桌面只在回环地址监听。双击托盘图标可打开管理窗口，查看连接配置、启停服务、切换兼容入口，以及设置 SFTP 根目录和 VNC 密码。

```powershell
go build -o bin/sshvnc.exe ./cmd/server
# 按 desktop/windows/README.md 构建 svnc-desktop.exe，准备配置和凭据后：
bin/sshvnc.exe -config state/config.yaml
```

[Windows 配置示例](configs/windows.example.yaml) 使用 `vnc.engine: libvncserver`。密码文件为 1–8 字节可打印 ASCII 明文，需要私有 NTFS ACL。以已登录用户的普通交互会话运行；OpenSSH 的 Session 0 不能直接共享该用户桌面。

UltraVNC 仍可通过 `vnc.engine: ultravnc` 使用：指定 `winvnc.exe`，移除 `password_file`，在同目录放置 `ultravnc.portable` 并按 [INI 示例](deployments/ultravnc.ini.example) 配置回环、认证和输入策略。原有实机结果见 Git 历史和 [LibVNC 对照记录](docs/libvnc-evaluation.md)。

## 配置与运行行为

- `ssh.allow_vnc` 同时控制 VNC subsystem、旧转发和可选 VNC 兼容入口的桌面授权。启用时，Agent 等待 RFB 握手标识后开始接收 SSH 会话。托管后端退出后，可从 Windows 托盘查看错误并重新启动服务。前台模式下 Agent 退出，由进程管理器按需恢复。使用 `vnc.engine: external` 时，由独立服务负责后端启停、仅回环监听和独立 VNC 认证。
- `vnc.view_only` 为托管后端启用只读桌面。LibVNCServer 在服务端拒绝键鼠和剪贴板写入；配置 UltraVNC 时同步设置 `InputsEnabled=0`。文本剪贴板走 VNC，文件传输走独立 SFTP。桌面只读与 SFTP 上传/下载权限分别配置。
- `sftp.mode` 支持 `disabled`、`read_only`、`write_only` 和 `read_write`。`write_only` 可上传、检查指定路径的元数据和管理目录中的文件，禁止下载与目录枚举。服务支持上传、下载、列目录、重命名、删除和创建目录。文件与目录以受限权限创建，禁止创建链接、变更属主和保留客户端文件属性。
- SFTP 默认使用 Agent 运行账户的 HOME；`root: "~"` 或省略 `root` 在 Linux/Windows 均表示用户主目录（Windows 为 `USERPROFILE`），自定义相对路径仍基于配置文件目录。客户端右侧显示实际上传位置，进入子目录后上传到当前目录。Go 的 `os.Root` 限制目录边界，打开文件时核验类型与硬链接计数。配置、密钥、VNC 密码和后端程序放在此目录之外；包含这些文件的根目录会被拒绝。单文件上传大小由 `max_file_size_mib` 限制，目录总容量由部署侧配额控制。
- SSH 限制同源 IP 的握手频率、认证尝试、连接和通道数量，并设置握手与空闲超时。SFTP 另有限制每个会话的打开句柄数。审计以 JSON 写到 stderr，通过 `connection_id` 和 `channel_id` 关联认证、转发、会话与文件操作。设置 `audit: false` 可关闭访问审计。

Go Agent 与 VNC 后端分别安装。本项目提供 Linux X11（LibVNCServer 或 TigerVNC）和当前登录 Windows 用户桌面的配置。Wayland、Windows 安全桌面、多租户桌面分配和跨 Viewer 剪贴板兼容性仍需后续实现或验收。

## JumpServer 与 guacd 接线

集成时应将 **SVNC（`svnc`）** 作为独立的资产接入类型，与普通 **VNC（`vnc`）** 区分。`svnc` 指向 SSH 入口，原生接入通过 `vnc` subsystem 获取 RFB；普通 `vnc` 指向裸 RFB 入口。端口均可配置，不能只凭端口号或产品名决定连接方式。这里是集成约定，尚不表示 JumpServer / guacd 已注册或实现 `svnc` 类型；模式、凭据和适配职责见[协议规范 §7.3](docs/protocol.md#73-jumpserver-与-guacamole-集成约定)。

下文描述现有兼容接线方式；原生 SVNC 适配可由代理直接打开 `vnc` subsystem，并向桌面处理端提供 RFB 字节流，无需暴露服务端后端端口。

5922 上保留原有 `direct-tcpip`，Guacamole 无需支持新的 VNC subsystem。代理为获授权的会话建立 SSH `-N -L`，在 guacd 可访问的同一网络命名空间分配一个本地回环端口。将 Guacamole VNC 的 `hostname` 和 `port` 指向这个端口，并提供独立的 VNC 密码。会话关闭或授权收回时，代理需关闭隧道。

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
