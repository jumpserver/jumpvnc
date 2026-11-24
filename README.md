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

### 2. 启动服务端

按目标机器的操作系统选择一种方式，并允许客户端访问服务端 TCP `5922`。服务端使用 SSH 公钥认证，需将客户端公钥加入服务端的 `authorized_keys`。

#### Linux

使用压缩包内置的 LibVNCServer + X11 后端，无需安装 TigerVNC 或 Go。目标系统需要兼容的运行库；Debian 13 可安装：

```bash
sudo apt-get install openssh-client libvncserver1 libx11-6 libxtst6 libxfixes3
```

解压服务端压缩包，在包含 `bin`、`backends` 和 `configs` 的目录打开 Bash。首次部署执行以下命令，将 `/path/to/client.pub` 换成客户端的 SSH 公钥路径：

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

/opt/jumpvnc/bin/sshvnc -config "$state/config.yaml"
```

在已登录的 X11 桌面中，以当前桌面用户运行最后一条命令即可启动，后续沿用已有配置和主机密钥。SFTP 默认访问运行用户的主目录；配置、密钥、密码和后端程序应放在 SFTP 根目录之外。

#### Windows

将服务端压缩包解压到 `%ProgramFiles%\SuperVNC`，按 [Windows 部署说明](desktop/windows/README.md#配置与部署) 准备 `service.yaml`、SSH 主机密钥、客户端公钥和 VNC 密码。在部署目录打开管理员 PowerShell：

```powershell
.\deployments\windows\install.ps1 -Agent .\bin\sshvnc.exe -Config .\service.yaml -Start
```

服务随系统启动，用户登录后自动显示管理托盘。双击托盘可查看状态、启停服务、修改密码或编辑配置。Windows 服务默认关闭文件传输；需要时在 `service.yaml` 中设置 `sftp.mode: read_write` 和明确的 `sftp.root` 文件根目录，再重启服务。

### 3. 连接桌面与传输文件

1. 在 Mac 上解压客户端，打开 `JumpVNC.app`，添加连接。
2. 协议选择 **SVNC**，填写服务器地址、端口 `5922` 和 SSH 用户名（示例配置为 `vncuser`）。客户端自动使用本机身份及已有 SSH 密钥配置；加密私钥需先加入 SSH agent。
3. 首次连接时核对服务器主机密钥指纹，再输入前面设置的 VNC 密码。
4. 进入桌面后，点击“文件管理”浏览远程目录，从 Finder 拖入文件上传，或选中文件下载到本机。文件管理需服务端已启用 SFTP。
5. 按需在“设置”中切换清晰或视频模式。当前窗口激活时，文本剪贴板自动双向同步。

更多操作见 [Viewer 使用说明](client/README.md)。已有 VNC Viewer 的 SSH 隧道接入方式见 [兼容接入](docs/protocol.md#71-ssh-本地转发)。

源码构建与测试见 [开发指南](docs/development.md)；协议与第三方集成见 [SVNC 规范](docs/protocol.md)。
