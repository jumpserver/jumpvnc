# LibVNCClient 原生对照客户端

macOS 上使用 LibVNCClient 0.9.15、SDL2 和 libjpeg-turbo 的实验客户端，连接 g15 的 LibVNCServer/DXGI 测试后端。不是完整的 svnc 客户端，没有 SFTP 和剪贴板集成。

## 运行

打开 `build/LibVNCClient Test.app`。依赖本机已安装的 Homebrew SDL2、jpeg-turbo 和 OpenSSL 共享库，暂未打包为可在其他 Mac 直接分发的安装包。本次构建为 Apple Silicon arm64。

默认连接 `127.0.0.1:15903`；该入口的 SSH 隧道沿用上一轮 `com.sshvnc.libvncserver-test` launchd 任务，校验 g15 Agent 的 SSH 主机密钥。VNC 密码仅在运行时从 `~/.ssh/sshvnc-g15/vnc-password.txt` 读取，不写入二进制或 app 包。

先断开 TurboVNC 的桌面连接，再测试本客户端。比较时使用相同内容和画质；并行 Viewer 会增加服务端编码、传输和网络争用。

- 默认 Q6（LibVNCServer 对应 JPEG Q79），压缩等级 1。
- `Control+Option+1`：Q3 流畅；`Control+Option+2`：Q6 均衡；`Control+Option+3`：Q9 清晰（服务端对应 JPEG Q100）。
- 标题中的 `updates/s` 是完成的 RFB 画面更新数，不是显示器 FPS。
- 标题中的 `input queue max` 仅是本客户端事件排队到发送调用的耗时，不是操作到远端画面呈现的延迟。

图像接收和解码在独立线程完成，完成一批 RFB 更新后才交给 SDL 绘制；键鼠使用另一条已认证的 RFB 连接，该连接不请求图像。这样键鼠发送不必等待图像读取和解码。两个连接都走同一个已有 SSH 转发入口，不开放新网络端口。服务端的串行编码和传输阻塞仍可能影响输入处理。

已实现普通键鼠、功能键、滚轮、保持比例的窗口缩放和画质切换；真实 UI 已验证鼠标点击和 F8 两个事件到达远端窗口。中文输入法组合输入、剪贴板、复杂快捷键与断线自动恢复仍待完善。不要据此判断已经替代成熟 Viewer。

## 构建

需要 CMake、C++17、pkg-config、Git、SDL2、jpeg-turbo 和 OpenSSL 3，例如已有 Homebrew 环境中运行：

```sh
brew install cmake pkg-config sdl2 jpeg-turbo openssl@3
./build.sh
```

脚本固定 LibVNCServer/LibVNCClient 0.9.15 的提交 `9b54b1ec32731bd23158ca014dc18014db4194c3`，把下载与构建产物放在忽略的 `build/`。可以通过 `LIBVNC_SOURCE_DIR`、`LIBVNC_BUILD_DIR` 指定已有源码与库的构建目录。

## 2026-11-19 延迟测量

`latency.cpp` 使用 LibVNCClient 发送 F8 或鼠标点击，使远端临时窗口中的黑白块切换，检测返回图像中相应像素的变化。计时从发送调用前开始，到该变化块解码完毕；不包含 SDL/GPU 显示和屏幕扫描。采样位置避开软件光标，防止光标黑色像素被误当作测试块颜色。

测试环境：g15 2560×1440 主屏，有动态游戏画面；原有 TurboVNC Q95/4:4:4 连接仍在。每个组合每种输入 12 次，均无超时。以下是原生 LibVNCClient 测量程序的结果，不是对 TurboVNC 的延迟测量，也不是两个 GUI Viewer 的公平基准。Q9 与 TurboVNC 的 fine quality 95 并不相同。

| 请求配置 | 输入 | 像素变化 P50 | 像素变化 P95 | 完整更新解码完毕 P50 |
| --- | --- | --- | --- | --- |
| Q9，全屏 | 键盘 | 218.6 ms | 419.9 ms | 284.9 ms |
| Q9，全屏 | 鼠标 | 235.0 ms | 385.4 ms | 352.0 ms |
| Q6，全屏 | 键盘 | 101.2 ms | 172.1 ms | 129.3 ms |
| Q6，全屏 | 鼠标 | 154.9 ms | 192.8 ms | 169.9 ms |
| Q6，仅 64×64 区域 | 键盘 | 90.8 ms | 124.3 ms | 90.8 ms |
| Q6，仅 64×64 区域 | 鼠标 | 111.5 ms | 148.6 ms | 111.5 ms |

P95 只是 12 次样本的经验估计。改变请求区域和质量都会改变服务端工作量；这些结果表明画质与全屏更新显著影响延迟，不能据此把问题仅归于 TurboVNC Viewer，也不能声称新 GUI 已经更快。此前约 0.30 秒首帧是可用性测试，不能当作操作延迟。

`latency-window.ps1` 是交互会话中的临时测试窗口，10 分钟后自动退出；运行时会在同目录写入 `latency-ready.txt`（坐标）和 `latency-result.txt`（收到的事件数）。按准备好的坐标执行：

```sh
./build/latency 15903 2138 226 6 0 12
# 参数：端口、点击 x、点击 y、画质 0–9、仅区域 0/1、样本数
```

本次临时 Windows 测试任务与窗口已清理。GUI 冒烟测试接收 57 次更新，输入连接认证成功；实际 UI 中鼠标点击和 F8 后远端计数变为 2，画质切换后窗口标题更新为 Q3，再恢复默认 Q6。
