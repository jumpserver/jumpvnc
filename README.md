# JumpVNC

**SSH + VNC，让远程桌面与文件上传下载共用一个安全连接。**

JumpVNC 是 JumpServer 团队开发的远程桌面工具：VNC 提供桌面操作，SSH 提供身份认证、加密与连接复用，SFTP 提供文件上传下载。在同一个客户端中即可操作远程桌面、同步文本和传输文件。

目前提供 macOS 客户端，以及 Windows、Linux 服务端。JumpVNC 是产品名，SVNC 是接入协议名，可独立使用，无需部署 JumpServer。

## 应用场景

- **远程办公与运维**：从 Mac 访问 Windows 或 Linux 桌面，操作业务软件、排查问题，并在本机与远程机器之间复制文本、上传和下载文件。
- **给 VNC 补上文件传输**：远程桌面操作时，直接将本机文件上传到远端，或将远端文件下载到本机，无需再单独打开文件传输工具。
- **简化网络接入**：桌面和文件共用一个 TCP 端口，适合希望减少防火墙开放端口、使用 SSH 公钥管理访问的环境。
- **无显示器的 Linux 桌面**：配合 Xvfb 提供远程图形环境，运行开发工具或图形化管理程序。见 [无显示器部署说明](deployments/linux/README.md)。

## 差异化特点

| 特点 | 使用体验 |
| --- | --- |
| **单端口** | SSH 承载 VNC 桌面与 SFTP 文件传输，默认只需开放 TCP `5922`。 |
| **文件传输** | 桌面窗口内浏览目录、拖放上传和下载文件，无需依赖 VNC 文件传输扩展。 |
| **SSH 认证** | 公钥认证、主机校验与传输加密，桌面和文件权限分别配置。 |
| **视频编码** | Windows 支持 H.264，优先使用硬件编码；文字办公可切换清晰模式。 |

Linux 当前支持 X11，Windows 共享控制台桌面；多个连接访问同一个桌面。目前不支持音频、多屏选择或 Wayland 桌面。

## 快速开始

### 1. 下载

打开 [GitHub Actions 构建记录](https://github.com/jumpserver/jumpvnc/actions/workflows/build.yml)，在成功构建的 **Artifacts** 中下载对应平台的压缩包。包内附带 SHA-256 校验文件。

| 用途 | 平台 | 下载产物 |
| --- | --- | --- |
| 客户端 | macOS（Apple Silicon / Intel） | `jumpvnc-viewer-macos-universal` |
| 服务端 | Linux（amd64 / arm64） | `jumpvnc-server-linux-amd64` / `jumpvnc-server-linux-arm64` |
| 服务端 | Windows（amd64） | `jumpvnc-server-windows-amd64` |

### 2. 安装服务端

以下使用下载包中的程序部署，无需安装 Go、Node.js 或编译工具。首次安装前，准备客户端的 SSH 公钥文件（如 `id_ed25519.pub`），私钥保留在客户端。服务端只需向客户端开放 TCP `5922`，内部 VNC 端口 `5900` 保持回环监听。

#### Windows：安装系统服务

**准备环境。** 目标机器需要 Microsoft Visual C++ x64 运行库，以及提供 `ssh-keygen` 的 Windows OpenSSH 客户端。用管理员权限将下载包中的 `bin`、`backends`、`configs`、`deployments` 等目录解压到 `C:\Program Files\SuperVNC`。以下命令在同一个**管理员 PowerShell** 窗口中执行，仅用于首次安装。

**准备配置与凭据。** 将 `C:\Temp\client.pub` 替换为客户端公钥的实际位置：

```powershell
$ErrorActionPreference = 'Stop'
Set-Location "$env:ProgramFiles\SuperVNC"

# 限制安装目录和文件的访问；安装脚本会单独授予托盘程序运行权限。
icacls.exe . /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /T
Copy-Item .\configs\windows.service.example.yaml .\service.yaml
Copy-Item C:\Temp\client.pub .\authorized_keys

ssh-keygen.exe -t ed25519 -f .\ssh_host_ed25519_key
```

生成的是**服务端主机密钥**，两次口令提示均直接按回车，不设置口令，以便系统服务自动启动。接着设置独立的 VNC 密码：

```powershell
$vncPassword = [System.Net.NetworkCredential]::new('', (Read-Host '设置 VNC 密码（1–8 个可打印 ASCII 字符）' -AsSecureString)).Password
if ($vncPassword -notmatch '^[\x20-\x7E]{1,8}$') { throw 'VNC 密码必须为 1–8 个可打印 ASCII 字符' }
Set-Content -LiteralPath .\vnc-password.txt -Value $vncPassword -Encoding ascii
Remove-Variable vncPassword

ssh-keygen.exe -lf .\ssh_host_ed25519_key.pub
```

保存最后输出的主机密钥指纹，供客户端首次连接时核对。`authorized_keys` 中配置的公钥用于 SSH 认证，VNC 密码用于桌面认证；SSH 用户名默认为 `vncuser`，不是 Windows 登录账号。

**启用文件上传下载。** 创建专用文件目录：

```powershell
New-Item -ItemType Directory -Force C:\JumpVNC-Files
icacls.exe C:\JumpVNC-Files /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F'
notepad.exe .\service.yaml
```

将 `service.yaml` 中的 `sftp` 部分替换为以下内容，保存后关闭编辑器：

```yaml
sftp:
  mode: read_write
  root: 'C:\JumpVNC-Files'
```

服务以 `LocalSystem` 运行，文件操作使用该服务账户的权限。文件根目录按实际需要配置，不会随登录用户自动切换；不要包含安装目录、配置或凭据。无需文件传输时，保留默认的 `mode: disabled`。

**安装并启动。** 保留示例配置中的回环 VNC 地址和关闭状态的普通 VNC 入口，然后执行：

```powershell
Get-ChildItem .\deployments\windows\*.ps1 | Unblock-File
.\deployments\windows\install.ps1 -Agent .\bin\sshvnc.exe -Config .\service.yaml -Start
Get-Service SuperVNC
```

状态应为 `Running`。服务显示名为 **JumpVNC Server**，内部服务名及安装目录沿用 `SuperVNC`。安装后开机自动运行，用户登录后自动显示管理托盘；也可从开始菜单打开 **JumpVNC Server**。

按实际访问来源配置防火墙和安全组。例如，仅允许本地子网访问：

```powershell
New-NetFirewallRule -DisplayName 'JumpVNC SSH' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 5922 -RemoteAddress LocalSubnet
```

跨网段或通过 VPN 访问时，将 `LocalSubnet` 改为实际客户端 IP 或网段。

**日常管理。** 双击托盘可查看状态、启停服务、设置文件目录、修改密码或编辑配置；配置操作需要管理员权限，修改后重启服务生效。退出托盘不会停止服务。

```powershell
Restart-Service SuperVNC
Get-Content .\service.yaml.service.log -Tail 50
Get-Content .\service.yaml.desktop.log -Tail 50
```

升级时先停止服务，备份安装目录，再替换程序并启动服务。保留 `service.yaml`、主机密钥、`authorized_keys` 和 VNC 密码，已有服务无需重复安装。Windows 登录或锁屏界面仍需输入 Windows 账号凭据；若要求 `Ctrl+Alt+Del`，在 Viewer 中选择“发送 Ctrl+Alt+Del”，并按 Windows 组策略允许服务发送该操作。

#### Linux：安装 X11 桌面服务

使用下载包内置的 LibVNCServer + X11 后端。目标系统需要兼容的运行库；Debian 13 可安装：

```bash
sudo apt-get install openssh-client libvncserver1 libx11-6 libxtst6 libxfixes3
```

在已登录的 X11 桌面中，以当前桌面用户打开 Bash。解压服务端压缩包，进入包含 `bin`、`backends`、`configs` 和 `deployments` 的目录。首次部署执行以下命令，将 `/path/to/client.pub` 换成客户端的 SSH 公钥路径：

```bash
sudo install -D -m 755 bin/sshvnc /opt/jumpvnc/bin/sshvnc
sudo install -D -m 755 backends/linux/svnc-desktop /opt/jumpvnc/backends/linux/svnc-desktop

umask 077
state=/var/lib/sshvnc/$USER
sudo install -d -m 700 -o "$USER" "$state"
cp configs/linux.example.yaml "$state/config.yaml"
sed -i 's|executable: backends/linux/svnc-desktop|executable: /opt/jumpvnc/backends/linux/svnc-desktop|' "$state/config.yaml"
ssh-keygen -t ed25519 -N '' -f "$state/ssh_host_ed25519_key"
cp /path/to/client.pub "$state/authorized_keys"
read -r -s -p '设置 VNC 密码（1–8 个可打印 ASCII 字符）: ' vnc_password
printf '\n'
printf '%s\n' "$vnc_password" > "$state/vnc-password.txt"
unset vnc_password
ssh-keygen -lf "$state/ssh_host_ed25519_key.pub"
```

保存主机密钥指纹，接着安装 systemd 用户服务，使其随图形会话运行并在异常退出后重启：

```bash
mkdir -p "$HOME/.config/systemd/user"
cp deployments/sshvnc.service "$HOME/.config/systemd/user/sshvnc.service"
sed -i 's|%h/.local/bin/sshvnc|/opt/jumpvnc/bin/sshvnc|' "$HOME/.config/systemd/user/sshvnc.service"
systemctl --user daemon-reload
systemctl --user import-environment DISPLAY XAUTHORITY
systemctl --user add-wants graphical-session.target sshvnc.service
systemctl --user start sshvnc.service
systemctl --user status sshvnc.service
```

此方式依赖已登录的 X11 图形会话，不提供登录前桌面。桌面环境需启动 `graphical-session.target` 并向用户服务导入 `DISPLAY` 和 `XAUTHORITY`；重新登录后应确认服务和桌面连接正常。无显示器或需开机即运行的独立桌面，见 [Xvfb 部署说明](deployments/linux/README.md)。

SFTP 默认访问运行用户的主目录，可在 `config.yaml` 中调整 `sftp.root` 和 `sftp.mode`。配置、密钥、密码和程序放在 SFTP 根目录之外。日常管理使用：

```bash
systemctl --user restart sshvnc.service
journalctl --user -u sshvnc.service -n 50
```

升级时先停止用户服务，备份后替换 `/opt/jumpvnc` 中的程序，保留 `/var/lib/sshvnc/$USER` 中的配置和凭据，再启动服务。

### 3. 连接桌面与传输文件

1. 在 Mac 上解压客户端，打开 `JumpVNC.app`，添加连接。
2. 协议选择 **SVNC**，填写服务器地址、端口 `5922` 和 SSH 用户名（示例配置为 `vncuser`）。客户端自动使用本机身份及已有 SSH 密钥配置；加密私钥需先加入 SSH agent。
3. 首次连接时核对服务器主机密钥指纹，再输入前面设置的 VNC 密码。
4. 进入桌面后，点击“文件管理”浏览远程目录，从 Finder 拖入文件上传，或选中文件下载到本机。文件管理需服务端已启用 SFTP。
5. 按需在“设置”中切换清晰或视频模式。当前窗口激活时，文本剪贴板自动双向同步。

更多操作见 [Viewer 使用说明](client/README.md)。已有 VNC Viewer 的 SSH 隧道接入方式见 [兼容接入](docs/protocol.md#71-ssh-本地转发)。

源码构建与测试见 [开发指南](docs/development.md)；协议与第三方集成见 [SVNC 规范](docs/protocol.md)。
