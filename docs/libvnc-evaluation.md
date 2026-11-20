# LibVNC 评估产物：g15，2026-11-19

后续正式后端接入见 [Windows LibVNCServer 后端](../desktop/windows/README.md)。本页保留早期对照实验的配置、反馈和测量限制。

本次在 Windows g15 上部署 LibVNCServer 0.9.15 + DXGI Desktop Duplication + libjpeg-turbo，Mac 使用 TurboVNC Viewer 和单独实现的 LibVNCClient/SDL2 客户端试用。作为实验分支保留，不替换现有 Go Agent 或 MyGo 客户端。

- [Windows 服务端与部署](../deployments/libvncserver-eval/README.md)：主屏抓取、变化块检测、键鼠注入、回环监听、计划任务和 SSH 隧道。
- [Mac 原生客户端与延迟记录](../client-libvnc/README.md)：图像和输入使用独立认证连接、画质切换、SDL 显示和测量程序。

## 评估结论

用户最终反馈 LibVNC 原生客户端“好像挺流畅”。这是主观试用反馈，尚不是同画质、单客户端情况下的性能胜出结论。原生客户端默认 Q6/JPEG Q79，而此前 TurboVNC 请求 fine quality 95、无色度抽样，两者画质不同。

原生测量程序在 TurboVNC 同时连接的情况下，高画质按键/点击到变化块解码 P50 约 219–235 ms；均衡约 101–155 ms。服务端仍串行抓屏、编码、处理网络事件；新客户端把输入发送与图像接收/解码分离，可以避免客户端解码挡住输入发送，但没有消除服务端和网络的等待。

## 保留范围

- 服务端源码、CMake、固定版本依赖准备、Windows 构建/启动/停止生命周期和测试配置。
- 客户端源码、固定版本构建脚本、Mac app 打包、输入到图像延迟程序与临时窗口脚本。
- 实机测试结果、测试端口和已知限制。
- 不提交密码、私钥、第三方源码、二进制、缓存和日志。

测试入口：Mac `127.0.0.1:15903` → g15 Agent `127.0.0.1:2223` → VNC `127.0.0.1:5902`。原 UltraVNC 2222/5900 保留。

下一轮库评估应保持抓屏方法、图像分辨率、客户端、请求区域和 JPEG 质量一致，并每次只保留一个有图像请求的 Viewer。继续分别测首帧、输入到图像和连续更新；不能把配置上限或 RFB 更新数当作显示器 FPS。
