# LibVNCServer Windows 桌面评估

这是实验性桌面适配程序，供 g15 对比现有 UltraVNC。LibVNCServer 本身是 RFB 库，没有 Windows 桌面抓屏服务端；本目录补上 DXGI Desktop Duplication 抓屏、64×64 变化块检测与 SendInput 键鼠输入。性能结果代表这套组合，不能单独代表协议库性能。

## 测试入口

| 项目 | UltraVNC 原入口 | LibVNCServer 测试入口 |
| --- | --- | --- |
| SSH 跳板机 | `g15` | `g15` |
| Agent 主机 | `127.0.0.1` | `127.0.0.1` |
| Agent SSH 端口 | `2222` | `2223` |
| SSH 用户 | `vncuser` | `vncuser` |
| VNC 端口 | `5900` | `5902` |
| SSH 主机别名 | `sshvnc-g15` | `sshvnc-g15` |

私钥、known_hosts 和 VNC 密码沿用原部署。当前客户端仅允许原端口组合自动读取已保存的密码；测试时取消「使用已保存的 g15 VNC 密码」，手动填写同一密码。可以在 Mac 终端执行以下命令复制密码，再粘贴到客户端：

```sh
tr -d '\r\n' < ~/.ssh/sshvnc-g15/vnc-password.txt | pbcopy
```

测试入口与原入口均只在 g15 回环地址监听，客户端通过现有 SSH 跳板连接。测试 Agent 复用原 SSH 主机密钥、公钥授权文件和 SFTP 交换目录。

## TurboVNC Viewer 入口

Mac 上已建立独立 SSH 隧道：`127.0.0.1:15903 → g15 Agent 2223 → 127.0.0.1:5902`。在 TurboVNC Viewer 的服务器地址填写 `127.0.0.1::15903`，输入原 VNC 密码。

注意原有本地端口 `15902` 实际转发到 g15 的 UltraVNC `5900`，不是 LibVNCServer。`15901` 是另一套 UltraVNC DD-engine 测试入口。

可运行本目录的 `start-tunnel.sh` 创建隧道。隧道由当前 Mac 用户的临时 launchd 任务 `com.sshvnc.libvncserver-test` 维持，不配置开机启动。如需停止：

```sh
launchctl remove com.sshvnc.libvncserver-test
```

## 构建与运行

需要 Windows x64、Visual Studio 2022 C++ Build Tools、Windows SDK、CMake 和 NASM。依赖源代码放在本目录的 `libvncserver/`、`jpeg/`、`zlib/`。本次固定版本：

- LibVNCServer 0.9.15，`9b54b1ec32731bd23158ca014dc18014db4194c3`
- libjpeg-turbo 3.2.1，`7b4993786d856cbccabfc0ed2f923ba000e326cc`
- zlib 1.3.2，`da607da739fa6047df13e66a2af6b8bec7c2a498`

先运行 `prepare-sources.sh` 获取并校验上述固定版本依赖；若 Windows 没有 Git，可在 Mac 准备源码后一起复制到部署目录。在 Windows 运行 `build.ps1` 生成 Release x64 可执行文件，启用 JPEG SIMD 和 zlib 压缩。把 1–8 字节 VNC 密码写入私有 `vnc-password.txt`，使用 NTFS ACL 限定运行用户和 SYSTEM 可读。首次运行 `install-task.ps1` 注册并启动手动计划任务，或通过当前已登录用户的交互会话运行 `run-test.ps1`；OpenSSH 的 Session 0 无法直接抓取用户桌面。

部署目录：`%LOCALAPPDATA%\sshvnc-agent\libvncserver-test`。测试计划任务为 `svnc-libvncserver-test`，手动启动，不配置开机触发器。查看 `desktop.log`、`desktop-error.log`、`agent.log`。

```powershell
Start-ScheduledTask -TaskName svnc-libvncserver-test
Stop-ScheduledTask -TaskName svnc-libvncserver-test
```

测试适配程序上限 60 次/秒抓屏，实际更新受客户端请求、编码与网络速度限制。只支持主屏、普通桌面、固定分辨率、非旋转显示器；更换分辨率后重启测试任务。没有实现剪贴板同步、安全桌面、虚拟屏、音频或生产服务恢复策略。文件操作仍使用原 SFTP 交换目录。

对比时使用同一客户端、同一画质档位、同一窗口大小和同一动态内容。客户端性能面板里的画面更新数是 RFB 更新数，不是显示器 FPS。不要把单次全屏加载时间当成连续操作延迟。

上游说明：[LibVNCServer](https://github.com/LibVNC/libvncserver)、[libjpeg-turbo](https://github.com/libjpeg-turbo/libjpeg-turbo)、[zlib](https://github.com/madler/zlib)。

## 2026-11-19 实机验证

- Release x64 构建成功，libjpeg-turbo SIMD 开启。
- 在当前用户 Session 2 中抓取到 2560×1440 主屏，图像可正常解码。
- 通过 `g15 → Agent SSH 2223 → VNC 5902` 完成 SSH 公钥验证、VNC 密码认证；错误 VNC 密码被拒绝。
- 测试窗口实际收到鼠标点击与键盘字符串 `libvnc-input-ok`，测试窗口和临时任务已清理。
- Tight/JPEG 全屏首次更新：90 个 JPEG 块，约 780 KB，约 0.30 秒；Raw 首次更新约 14.75 MB、2.47 秒。只是单次功能验证样本，不能据此推断 FPS 或相对 UltraVNC 的性能。
- 测试任务的启动器使用独立 Windows Job；停止任务会关闭 SSH Agent 和桌面后端，重启可恢复入口。
- 原 UltraVNC 的 2222/5900 入口保留。
