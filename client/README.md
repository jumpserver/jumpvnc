# SSH VNC 桌面客户端

macOS 第一版使用 MyGo 窗口和 noVNC 画布。Go 连接 SSH Agent，随后在同一连接中打开 VNC 转发通道和 SFTP 会话。应用仅给内置的 VNC 画布建立临时回环 WebSocket。文件侧栏显示 Agent 配置的交换目录。

## 构建

需要 Go 1.27.3 或更新版本、Node.js 和 npm。客户端使用独立的 Go 模块。

```bash
cd client
npm --prefix frontend ci
go tool mygo build -skip-dmg
```

在 Apple Silicon Mac 上，打开 `client/build/darwin-arm64/SSH VNC.app`。也可用 `go tool mygo build -platform darwin/universal -skip-dmg` 构建通用版本。

## 连接和传输

- 当前 Mac 上若有之前配置的 `~/.ssh/sshvnc-g15` 密钥、主机公钥和 VNC 密码，应用会预填 `g15` 连接。点「连接」后，客户端建立自己的 SSH 连接。跳板机沿用本机 OpenSSH 配置和主机校验。
- 连接其他主机时，填写 Agent 地址、SSH 用户、VNC 密码，并在「SSH 与 VNC 设置」中填写私钥和可信的 `known_hosts`。当前支持可直接读取的未加密 SSH 私钥。Agent 与跳板机分别校验主机密钥。
- 打开侧栏目录后，从 Finder 拖入普通文件，或点击「上传」。拖放后可核对文件名和目标目录，再开始上传。同名文件保留原样，上传失败时面板显示原因。
- 选中文件后点击「下载」或双击文件，使用系统保存对话框选择位置。侧栏也支持刷新目录和新建文件夹。

前端包含 noVNC 1.7.0，构建会随应用附带其许可说明。
