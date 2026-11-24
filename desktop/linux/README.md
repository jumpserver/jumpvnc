# JumpVNC Server Linux X11 桌面后端

`svnc-desktop` 使用固定版本 LibVNCServer 0.9.15 提供 RFB 服务，Xlib 读取当前 X11 桌面，XTEST 注入键鼠，XFIXES 获取光标和剪贴板变化。Go Agent 仍负责 SSH、公钥授权、审计、SFTP 和后端生命周期。

客户端视频模式优先协商 H.264（RFB 编码 50），沿用现有 SSH 桌面通道，无需额外端口。后端通过 FFmpeg 优先尝试 NVIDIA NVENC，再尝试当前用户可访问的 VAAPI render 节点；初始化失败、编码失败或编码器缓冲画面时，自动回退 `libx264` 软件编码。硬编运行失败时用当前画面重建软件 IDR，并通知客户端重置解码器。软件编码也失败时先发一个完整 Raw 矩形，再恢复普通 RFB 编码。

视频使用 Baseline、4:2:0、BT.709、无 B 帧，目标平均 6 Mbps、最高 30 FPS；目标码率不是严格的瞬时上限，没有自动带宽调节。只在画面变化或客户端请求刷新时编码，不累积历史画面队列。最多两个视频客户端；不支持视频的客户端、奇数分辨率或超过 4096×2160 的桌面继续使用 Tight/Raw 等协商编码。分辨率变化时重建编码器。X11 采集和 BGRA → YUV 转换仍在 CPU 上，没有实现 GPU 直接采集或零拷贝。

## 构建和部署

需要 C++17、CMake、pkg-config、FFmpeg 5.1+（libavcodec 59+，含 `libx264` 编码器）、JPEG/zlib，以及 X11、XTEST 和 XFIXES 开发包。LibVNCServer 使用固定提交源码构建并应用编码 50 钩子，不再链接系统 LibVNCServer。Debian 13 可安装：

```sh
sudo apt-get install ca-certificates git build-essential cmake pkg-config \
  libjpeg62-turbo-dev zlib1g-dev libavcodec-dev libavutil-dev libswscale-dev \
  libx11-dev libxtst-dev libxfixes-dev
sh desktop/linux/prepare-sources.sh
cmake -S desktop/linux -B /tmp/svnc-linux-build -DCMAKE_BUILD_TYPE=Release
cmake --build /tmp/svnc-linux-build -j
sudo cmake --install /tmp/svnc-linux-build --prefix /opt/svnc
go build -o bin/jumpvnc ./cmd/server
```

准备私有配置和凭据（首次部署执行，保留已有 SSH 主机密钥）：

```sh
umask 077
state=/var/lib/jumpvnc/$USER
sudo install -d -m 700 -o "$USER" "$state"
cp configs/linux.example.yaml "$state/config.yaml"
# 将配置中的 vnc.executable 设置为 /opt/svnc/backends/linux/svnc-desktop。
ssh-keygen -t ed25519 -N '' -f "$state/ssh_host_ed25519_key"
cp /path/to/client.pub "$state/authorized_keys"
printf 'changeMe\n' > "$state/vnc-password.txt"
./bin/jumpvnc -config "$state/config.yaml"
```

示例密码必须换成自己的密码。后端密码文件是 1–8 字节可打印 ASCII 明文，可带一个结尾换行；与 TigerVNC 的 `vncpasswd` 文件不同。文件需为普通文件且组/其他用户无权限，后端拒绝符号链接。配置路径相对于 YAML 文件所在目录。SFTP 默认访问运行账户的 HOME，配置、凭据和后端程序须在 HOME 外；旧交换目录中的文件保留在原位置。

以已登录桌面的普通用户运行，继承 `DISPLAY` 和 `XAUTHORITY`。可以沿用 [systemd 用户服务](../../deployments/jumpvnc.service)，从桌面终端导入环境后启动。仅监听 `127.0.0.1`，远程客户端仍通过 SSH 隧道接入；文件传输仅走专用 SFTP 目录。LibVNCServer 静态链接；FFmpeg、JPEG/zlib 和 X11 运行库采用动态链接，目标系统需安装与构建环境匹配的版本。Debian 13 对应 `libavcodec61 libavutil59 libswscale8 libjpeg62-turbo zlib1g libx11-6 libxtst6 libxfixes3`。NVENC/VAAPI 另需相应 GPU 驱动及设备访问权限，程序不会修改权限。发布包附带固定版本 LibVNCServer 源码和许可证；离线构建可把该源码解压至 `desktop/linux/libvncserver`，或用 `SVNC_DEPENDENCIES` 指定包含该目录的路径。

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

隔离环境安装 `ffmpeg xvfb xterm xdotool xauth xclip tigervnc-tools tigervnc-viewer openssh-client iproute2 xfonts-base`。以非 root 用户运行，确保测试端口 5922、5900、15900 空闲：

```sh
cmake --build /tmp/svnc-linux-build --target h264-smoke
/tmp/svnc-linux-build/h264-smoke software
/tmp/svnc-linux-build/h264-smoke auto
go test -c -o /tmp/svnc-rfb.test ./tests/integration
SVNC_RFB_TEST_BINARY=/tmp/svnc-rfb.test SVNC_TEST_H264=1 \
  bash tests/integration/libvncserver.sh bin/jumpvnc /tmp/svnc-linux-build/svnc-desktop
SVNC_RFB_TEST_BINARY=/tmp/svnc-rfb.test SVNC_TEST_H264_ODD_GEOMETRY=1 \
  SVNC_TEST_DESKTOP_GEOMETRY=801x601x24 \
  bash tests/integration/libvncserver.sh bin/jumpvnc /tmp/svnc-linux-build/svnc-desktop
```

该测试使用临时 Xvfb 桌面和真实 TigerVNC Viewer，检查 SSH 白名单、VNC 认证、键鼠、中文文本剪贴板双向传输、SFTP、退出和 SSH 主机密钥持久化。可选编译 `go test -c -o /tmp/svnc-rfb.test ./tests/integration`，设置 `SVNC_RFB_TEST_BINARY=/tmp/svnc-rfb.test` 后运行上述脚本，额外检查错误密码、拒绝 None 认证、禁用 RFB 文件传输、不完整握手回收、Raw 画面、光标及 ExtendedClipboard 协商。

`h264-smoke` 使用合成画面验证 180 帧即时编码/解码、色彩通道和范围、分辨率变化与编码器重建；`auto` 同时验证无 GPU 环境的软件回退。协议测试检查编码协商、完整首帧、上下文重置、模式切换、双客户端上限、断线回收及实际 SSH/RFB 码流解码。奇数尺寸测试验证普通编码回退。在有 GPU 的真实 Linux 桌面上可运行 `h264-smoke hardware-fallback` 注入硬编故障，验收软件 IDR 回退；容器验证不代表 NVENC/VAAPI 驱动实测。编码器选择见 `h264.ready` 日志，运行统计见 `h264.stats`。FFmpeg 接口参考 [编码 API](https://ffmpeg.org/doxygen/7.0/group__lavc__encdec.html)。

2026-11-24 本地 Debian 13 arm64 和 amd64 隔离环境构建通过，两种架构分别验证 180 帧即时编解码（含 802×602 非宏块对齐尺寸）。arm64 的 Xvfb 实际 SSH/RFB 传输、H.264 解码、编码切换、客户端限制、断线回收、奇数尺寸回退、键鼠、剪贴板和 SFTP 均通过。amd64 在容器模拟环境运行；本轮没有真实 Linux GPU，不能据此确认硬编性能。

本目录代码采用 GPL-2.0-or-later。分发时附带 COPYING 并提供对应源码/构建脚本，依赖许可与源码要求按所用发行版/构建产物准备。
