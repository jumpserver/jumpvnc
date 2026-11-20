# SVNC Linux X11 桌面后端

`svnc-desktop` 使用 LibVNCServer 编码和提供 RFB 服务，Xlib 读取当前 X11 桌面，XTEST 注入键鼠，XFIXES 获取光标和剪贴板变化。Go Agent 仍负责 SSH、公钥授权、审计、SFTP 和后端生命周期。

## 构建和部署

需要 C++17、CMake、pkg-config、带 JPEG/zlib 支持的 LibVNCServer 0.9.15 或更新版本，以及 X11、XTEST 和 XFIXES 开发包。Debian 13 可安装：

```sh
sudo apt-get install build-essential cmake pkg-config libvncserver-dev libx11-dev libxtst-dev libxfixes-dev
cmake -S desktop/linux -B /tmp/svnc-linux-build -DCMAKE_BUILD_TYPE=Release
cmake --build /tmp/svnc-linux-build -j
cmake --install /tmp/svnc-linux-build --prefix "$PWD/state"
go build -o bin/sshvnc ./cmd/server
```

准备私有配置和凭据（首次部署执行，保留已有 SSH 主机密钥）：

```sh
umask 077
mkdir -p state/exchange
cp configs/linux.example.yaml state/config.yaml
ssh-keygen -t ed25519 -N '' -f state/ssh_host_ed25519_key
cp /path/to/client.pub state/authorized_keys
printf 'changeMe\n' > state/vnc-password.txt
./bin/sshvnc -config state/config.yaml
```

示例密码必须换成自己的密码。后端密码文件是 1–8 字节可打印 ASCII 明文，可带一个结尾换行；与 TigerVNC 的 `vncpasswd` 文件不同。文件需为普通文件且组/其他用户无权限，后端拒绝符号链接。配置路径相对于 YAML 文件所在目录。

以已登录桌面的普通用户运行，继承 `DISPLAY` 和 `XAUTHORITY`。可以沿用 [systemd 用户服务](../../deployments/sshvnc.service)，从桌面终端导入环境后启动。仅监听 `127.0.0.1`，远程客户端仍通过 SSH 隧道接入；文件传输仅走专用 SFTP 目录。分发采用动态链接，目标系统需要相应的 LibVNCServer 和 X11 运行库。

## 范围和限制

- 共享默认 X screen 的整个根窗口；支持 TrueColor 像素格式。后端不自行创建虚拟桌面，也不提供显示器选择或登录界面。无显示器机器可由部署侧使用 [Xvfb + Xfce](../../deployments/linux/README.md) 提供桌面。
- 键鼠、垂直/水平滚轮、真实光标形状/位置；每个连接跟踪按键/按钮，断线释放其持有的输入。键盘使用当前 XKB 映射，未映射的 Unicode 字符需通过剪贴板粘贴。
- `view_only: true` 禁止键鼠和客户端剪贴板写入，SFTP 权限独立配置。
- 双向 UTF-8 文本剪贴板，兼容旧客户端 Latin-1。只共享 `CLIPBOARD`，不共享 `PRIMARY`、文件或图像。文本上限为 64 KiB，不支持 X11 INCR 大文本传输。
- 根窗口尺寸变化使用 NewFBSize 通知支持的客户端；不支持的客户端需重连。X 连接丢失会使后端退出，由 Agent/进程管理器恢复。
- 最多约每 33ms 全屏读取一次，64×64 分块比较后只标记变化区域。尚未采用 MIT-SHM/XDamage，也没有建立性能优势结论。
- **本实现只支持 X11。** Wayland 会话被明确拒绝，不能借助 XWayland 共享整个 Wayland 桌面。Wayland 后续需要独立的 Portal/PipeWire 抓屏及输入适配；继续复用 LibVNCServer 和 Go Agent。
- 尚需在目标发行版和实体桌面验收多屏布局、不同键盘布局、锁屏/解锁及休眠恢复。Xvfb 验收不等同于这些场景的实机验收。

后端使用 Linux parent-death signal 跟随 Agent 退出，也支持 SIGINT/SIGTERM。托管后端退出会导致 Agent 退出，避免保留失效的桌面入口。

## 验证

隔离环境安装 `xvfb xterm xdotool xauth xclip tigervnc-tools tigervnc-viewer openssh-client iproute2 xfonts-base`。以非 root 用户运行，确保测试端口 2222、5900、15900 空闲：

```sh
bash tests/integration/libvncserver.sh bin/sshvnc /tmp/svnc-linux-build/svnc-desktop
```

该测试使用临时 Xvfb 桌面和真实 TigerVNC Viewer，检查 SSH 白名单、VNC 认证、键鼠、中文文本剪贴板双向传输、SFTP、退出和 SSH 主机密钥持久化。可选编译 `go test -c -o /tmp/svnc-rfb.test ./tests/integration`，设置 `SVNC_RFB_TEST_BINARY=/tmp/svnc-rfb.test` 后运行上述脚本，额外检查错误密码、拒绝 None 认证、禁用 RFB 文件传输、不完整握手回收、Raw 画面、光标及 ExtendedClipboard 协商。

本目录代码采用 GPL-2.0-or-later。分发时附带 COPYING 并提供对应源码/构建脚本，依赖许可与源码要求按所用发行版/构建产物准备。
