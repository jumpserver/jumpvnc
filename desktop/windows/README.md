# JumpVNC Server Windows 桌面后端

`svnc-desktop.exe` 使用 LibVNCServer 0.9.15、libjpeg-turbo 和 DXGI Desktop Duplication，共享当前登录用户的主屏。Go Agent 管理后端生命周期，并提供 SSH 转发、SFTP、授权和审计。文件传输只使用 SFTP；后端禁用 UltraVNC 文件传输，不编译 TightVNC 文件扩展。

支持由客户端显式选择的 H.264 试用模式：RFB 编码 50（与 LibVNCServer 头文件中的 `rfbEncodingH264` 不是同一种封装），通过原 SSH 桌面通道传输。优先枚举 Media Foundation 硬件编码器，初始化或运行失败时回退 Windows 内置的同步软件编码器。图像使用 NV12 / 4:2:0、Baseline、无 B 帧，平均码率目标 6 Mbps、最高 30 FPS；尚未提供自适应码率或 UDP 媒体通道。请求码率和驱动回读不代表严格的输出上限，实际流量应单独采样。只在画面变化或客户端请求刷新时编码，最多两个视频客户端，支持正偶数尺寸且不超过 4096×2160；不满足条件时保留原 RFB 编码协商。软件编码也失败时先发送一个 Raw 矩形以保持已宣布的矩形数量一致，随后回退普通编码。

硬件 MFT 解锁异步接口后按 NeedInput / HaveOutput 事件处理，应用最多保留一幅在途画面；首帧事件等待上限为 1 秒，后续为 200 ms，超时回退软件。驱动 API 调用本身不受这些事件等待上限约束。失败后销毁硬件实例，用同一幅 NV12 图像重试软件 IDR，并通知 RFB 重置客户端解码上下文。格式协商后重新应用码率控制并记录可用的属性回读。采集回读和 BGRA → NV12 转换仍在 CPU 上，没有实现 DXGI 纹理直接送编码器。当前实机验证了 NVIDIA RTX 3060 Laptop GPU；其他厂商驱动需单独验收。

`h264.cmake` 为固定版本的 LibVNCServer 在构建目录生成编码协商/发送的覆盖源文件，不修改依赖检出；原源码钩子不匹配时构建失败。每个视频客户端独立维护编码上下文，重连、重新协商、尺寸改变或硬件回退时重建，并在新编码流首帧携带解码上下文重置标志。完整帧按顺序编码，不排队保存历史画面。`h264.config`、`h264.ready`、`h264.hardware_unavailable`、`h264.hardware_failed`、`h264.unavailable`、`h264.failed`、`h264.stats` 日志可用于排查编码器、码率和编码耗时；`hardware=1` 表示当前使用硬件 MFT，`hardware=0` 表示软件。

## 构建

在 macOS/Linux 运行 `./prepare-sources.sh` 准备固定提交的依赖源码；Windows 也可以用 Git 按脚本中的提交准备 `libvncserver`、`jpeg`、`zlib` 三个目录。依赖与构建产物均不提交。

Windows x64 需要 Visual Studio 2022 C++ Build Tools、Windows SDK、CMake、NASM。在本目录运行：

```powershell
.\build.ps1
# CMake 不在 PATH 时：
.\build.ps1 -CMakeCommand 'C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin\cmake.exe'
```

可用 `-Dependencies C:\path\to\sources` 指向已准备的源码。输出为 `build\Release\svnc-desktop.exe`。构建使用动态 MSVC CRT，目标 Windows 需要匹配的 x64 Visual C++ 运行库。不要使用实验目录的旧二进制；正式后端有独立的命令行参数和权限控制。

## 配置与部署

SFTP 默认以运行账户的 `USERPROFILE` 为根目录，配置写 `root: "~"` 或省略 `root` 即可。Agent 配置、主机私钥、VNC 密码和桌面后端需放在 HOME 外，例如 `C:\ProgramData\SVNC`；仍支持明确配置其他根目录。右侧文件栏显示实际上传位置，进入子目录后上传到当前目录。

在仓库根目录构建 Agent：

```powershell
go build -o bin\sshvnc.exe .\cmd\server
```

以存放配置文件的目录为基准，将后端放到 `backends\windows\svnc-desktop.exe`。复制 `configs/windows.example.yaml`，准备 SSH 主机密钥、公钥授权文件和 `vnc-password.txt`。密码文件是 **1–8 字节可打印 ASCII 明文**，允许结尾一个 LF/CRLF，不是 TigerVNC/UltraVNC 的加密密码文件。密码不传入命令行，也不写入日志。用 NTFS ACL 限定运行账户和 SYSTEM/管理员读取配置、密钥及密码；交换目录不要包含这些文件。

```yaml
vnc:
  engine: libvncserver
  listen: "127.0.0.1:5900"
  executable: backends/windows/svnc-desktop.exe
  password_file: vnc-password.txt
  view_only: false
```

在已登录用户的普通交互会话运行 Agent。可以从该桌面启动终端，也可以使用计划任务：

```powershell
.\deployments\windows\install.ps1 -Agent C:\svnc\sshvnc.exe -Config C:\svnc\config.yaml -Start
# 明确需要登录时自动运行时，加 -AtLogon；默认仅注册手动任务。
```

任务以当前用户的 Limited 权限运行。安装脚本不覆盖已有任务；更新部署前先停止并删除对应任务，再注册。任务使用 Windows Job 管理 Agent 进程树，停止任务会回收后端。Agent 正常退出会停止后端；后端也监测 Agent PID，防止直接启动 Agent 后异常退出留下后端。

### 托盘控制

已安装 Windows 系统服务时，在交互式桌面直接启动 Agent 会自动进入服务管理模式，不再启动另一份桌面后端。未安装系统服务时，保留原有的独立托盘模式。双击通知区域中的 `JumpVNC Server` 图标即可打开管理窗口。窗口显示服务状态和当前 YAML 配置文件的路径，可以启动、停止、重启服务，或用记事本编辑配置。保存后重启服务即可应用新配置。如果服务已停止，选「启动服务」。启动失败或后端意外退出时，可选「查看错误」后重试。

以下 SFTP 和兼容入口设置用于独立托盘模式；系统服务管理窗口会禁用这两项。

「连接配置」显示配置中的 SSH 监听、VNC 后端、普通 VNC 兼容入口和 SSH 登录用户名，也显示实际启动 Agent 的 Windows 运行用户。SSH 登录用户名可通过 YAML 的 `ssh.user` 修改，客户端连接时需使用新用户名。Windows 运行用户由启动 Agent 的桌面会话或计划任务决定。用「开启兼容」或「关闭兼容」可保存普通 VNC 入口开关，重启后生效。兼容入口使用 VNC 密码，流量未经 SSH 加密。

启用 SFTP 时，可以输入或浏览选择现有根目录，也可用「使用 HOME」填入默认值 `~`，再选「保存目录」。窗口同时显示解析后的路径。相对路径以 YAML 文件所在目录为准，`~` 指 Windows 运行用户的 `USERPROFILE`。监听地址和 SFTP 访问模式可通过「编辑配置」在 YAML 中修改。

使用托管的 LibVNCServer 后端时，「显示密码」可以查看当前密码，切走或关闭窗口会再次遮住。在「新密码」中输入 1–8 个可打印 ASCII 字符，再选「保存密码」，即可更新后端的密码文件。更改目录或密码后，重启服务生效。

关闭管理窗口后，Agent 继续驻留在托盘。右键菜单也可打开管理窗口、控制服务和查看错误。停止服务会断开现有桌面和 SFTP 会话。要结束 Agent，可在右键菜单中选「退出 JumpVNC」。

需要由计划任务在后端故障后自动重试时，注册任务时加 `-NoTray`。直接运行 Agent 时可加 `-no-tray`。非交互会话也使用此运行方式，服务失败时进程以非零状态退出。

日志位于配置旁的 `<配置名>.agent.log`、`<配置名>.stdout.log` 和 `<配置名>.launcher-error.log`。注册启动器前，将部署目录的写权限限制给该账户和管理员。

### Windows 系统服务

服务模式由 Windows 服务控制管理器开机启动。Agent 在 Session 0 运行，`svnc-desktop.exe` 在 Windows 控制台会话里跟随当前输入桌面。服务模式使用 DXGI 抓屏，可在 DXGI 初始化失败、抓屏中断或没有首帧时回退到 Windows GDI。两种抓屏方式都依赖 Windows 提供可捕获的控制台画面。

服务显示名称为 `JumpVNC Server`；内部服务名 `SuperVNC` 和安装目录沿用旧标识，以兼容已有部署。

安装脚本默认同时配置登录后自动显示托盘，以及开始菜单中的 **JumpVNC Server** 入口。托盘读取系统服务的实际状态，可启动、停止、重启服务、修改 VNC 密码、编辑 `service.yaml` 和查看服务日志；关闭管理窗口只隐藏窗口，退出托盘或注销用户不会停止后台系统服务。配置和密码修改后需重启服务。普通用户可以查看服务状态；点击「以管理员身份管理」经 Windows UAC 授权后进行控制和配置。SFTP 与普通 VNC 入口继续保持关闭。

已有服务可以单独安装托盘登录入口：

```powershell
.\deployments\windows\install-service-tray.ps1 -Agent .\bin\sshvnc.exe
# 立即打开管理窗口：
.\bin\sshvnc.exe -manage-service -open-panel
```

托盘总是使用 Windows 服务注册的配置路径，`-config` 不会更改它管理的文件。同一用户、同一桌面和权限级别只保留一个管理托盘，重复打开管理窗口会激活已有窗口。安装时加 `-NoTray` 可只安装后台服务。


用管理员权限把发布包解压到 `%ProgramFiles%\SuperVNC`。将 [服务模式示例](../../configs/windows.service.example.yaml) 复制为部署目录下的 `service.yaml`，并准备 SSH 主机密钥、`authorized_keys` 和 `vnc-password.txt`。服务以 `LocalSystem` 运行，限制部署目录、配置引用的可执行文件及密钥文件的 ACL，只允许管理员和 SYSTEM 修改。服务模式要求使用 SSH/SVNC 和 `libvncserver` 后端，配置中关闭普通 VNC 兼容入口与 SFTP。回环后端只接受来自 Agent 进程的连接。若已有托盘或计划任务占用同一端口，先停止相应实例。

在部署目录打开管理员 PowerShell：

```powershell
.\deployments\windows\install-service.ps1 -Agent .\bin\sshvnc.exe -Config .\service.yaml -Start
Get-Service SuperVNC
```

用 SVNC 连接。Windows 停留在登录或锁屏界面时，在原生界面输入 Windows 凭据。若目标要求 `Ctrl+Alt+Del`，使用 Viewer「设置」中的「发送 Ctrl+Alt+Del」，并在 Windows 组策略的「Windows 登录选项」里允许服务发送软件安全注意序列。服务通过 Windows `SendSAS` API 请求该操作，由 Windows 执行组策略。Agent 和抓屏日志分别写入 `service.yaml.service.log` 与 `service.yaml.desktop.log`。

在目标机断开物理显示器并冷启动，全程不连接 RDP，依次验证登录界面、登录、锁屏与解锁、注销后的重新登录。还需验证崩溃重启与手动停止服务后端口回收。自动填写或保存 Windows 凭据尚未实现。

## 支持范围

- 仅 IPv4 回环监听，使用独立 VNC 密码认证，SSH 承担远程传输加密。
- 主屏画面、普通键鼠、滚轮；每个连接独立跟踪按键状态，断线释放该连接持有的按键。
- `view_only: true` 在服务端拒绝键鼠和客户端剪贴板写入，仍可查看画面和接收文本剪贴板。SFTP 的上传/下载权限通过 `sftp.mode` 单独配置。
- 双向文本剪贴板：ExtendedClipboard 支持 UTF-8，旧客户端使用 Latin-1 回退；不传文件、HTML 或图像。JumpVNC 客户端在当前桌面窗口激活时自动双向同步系统文本剪贴板，也可通过“剪贴板”窗口手动发送或复制文本。
- Windows 光标形状、热点、隐藏状态和位置通过 RFB 传输。半透明边缘与老式反色光标使用掩码近似，尚未覆盖所有自定义/动画光标。
- DXGI 访问丢失后保留连接并清空旧画面，重试普通桌面抓屏。主屏分辨率变化通过 NewFBSize 通知支持的客户端，不支持的客户端需要重连。

尚不支持多屏选择、旋转屏、虚拟屏、音频或 Windows 用户会话隔离。登录界面和安全桌面使用系统服务模式。普通交互任务在用户注销时结束，后续登录需手动启动或配置 `-AtLogon`。普通权限下的 `SendInput` 受 UIPI 限制。锁屏、休眠和分辨率变化仍需在目标设备做实机验收。

抓屏最多约每 16ms 尝试一次，实际吞吐受画面变化、客户端请求、编码和网络影响。当前保持单线程持有 LibVNCServer 与帧缓冲，避免并发修改造成画面撕裂；尚未进行线程拆分和 DXGI dirty/move regions 优化，不宣称相比 UltraVNC 更快。

## 许可与依赖

本目录代码与 LibVNCServer 使用 GPL-2.0-or-later。发布二进制时附带 COPYING，以及按许可证提供的对应源码和构建脚本；libjpeg-turbo、zlib 的许可文件也应随包提供。`cmake --install build --config Release --prefix C:/package` 可安装后端和 GPL 许可，完整分发仍需准备依赖许可及对应源码。

## 验证入口

仓库根目录运行 `go test -race ./...` 和 `go vet ./...`，客户端模块分别运行相同命令，并执行前端测试与 MyGo 构建。

在本机建立到测试后端的 SSH 隧道后，可显式运行真实 RFB 验收；正常单元测试不会访问远端桌面：

```sh
SVNC_RFB_ADDRESS=127.0.0.1:15905 \
SVNC_RFB_PASSWORD_FILE=/private/path/vnc-password.txt \
go test ./tests/integration -run TestLibVNCBackend -v -count=1
```

该验收验证认证、无认证请求拒绝、禁用 RFB 文件传输、不完整握手回收、分包协商、Raw 画面、光标与 ExtendedClipboard 能力。真实键鼠、中文剪贴板双向同步及只读策略需要在独立临时测试窗口验收，避免向其他应用发送测试输入。LibVNCServer 0.9.15 的剪贴板广播 API 不过滤未认证客户端，本适配层在认证完成前延迟广播，并在 10 秒后回收未完成的握手。

## 2026-11-24 H.264 试用验收

硬件编码补充：

- g15 默认选择 `NVIDIA H.264 Encoder MFT`，实际桌面会话的 `h264.ready` / `h264.stats` 均确认 `hardware=1`，同时观察到 NVIDIA 编码引擎有负载。客户端画面正常，采样约 4–5 Mbps、每秒约 23–25 次画面更新，绘制队列与未解析数据为 0。与此前软件编码观察的画面内容不同，不作为严格性能对比。
- 同一 2560×1440 合成任务、120 帧：软件每帧进程 CPU 时间约 36 ms，硬件多次采样约 10–13 ms（包含合成图像更新和色彩转换）。短流媒体速率软件约 6.09 Mbps、硬件约 8.08 Mbps，说明 6 Mbps 的请求和回读不是实际输出硬上限；没有为了达到码率目标丢弃已编码的参考帧。
- 严格硬件模式全程 120 帧均未回退；在第 60 帧已经提交硬件后注入故障，自动软件重试成功，编码流代次增加，重试帧包含 SPS/PPS/IDR，合计仍输出 120 帧。FFmpeg 解码和 noVNC 访问单元解析均为 120 帧。故障注入只编入 `h264-smoke`，不编入正式后端。
- 硬件临时后端的真实 RFB 验收通过：首帧、后续帧、两轮 H.264 → Raw → H.264 切换，以及原有认证拒绝和 Raw/光标/剪贴板协商。
- 升级前的软件编码后端及 `restore.ps1` 保存在 g15 的 `C:\Program Files\SuperVNC\backups\before-hardware-h264-20261124-130134`。

最初的软件编码试用记录：

- Windows 后端交叉构建、macOS Universal 客户端构建通过。
- g15 软件编码合成验收：2560×1440 桌面中一个 960×540 区域持续变化，120 帧全部输出，媒体平均约 6.09 Mbps。FFmpeg 完整解码 120 帧通过；客户端访问单元解析也得到 120 帧（含两个关键帧）。此数据不是游戏性能对比。
- 真实回环 SSH 隧道验收通过：编码 50 首帧的 SPS/PPS/IDR、后续帧、两轮 H.264 → Raw → H.264 切换；原有认证拒绝、文件扩展拒绝、未完成握手回收、Raw/光标/剪贴板能力验收通过。
- Mac MyGo / noVNC 真实显示了 g15 的游戏窗口：当时均衡档接收约 27–34 Mbps，视频档约 5–6 Mbps、每秒约 24 次画面更新，采样时绘制队列和未解析数据均为 0。动态画面没有严格重放，这只是同一会话的观察，不能视为通用性能保证；未测量输入到显示的延迟。
- g15 只替换已安装的 Windows 后端，原文件和 `restore.ps1` 保存在 `C:\Program Files\SuperVNC\backups\before-h264-20261124-123253`。SSH 入口和凭据沿用原部署。

合成编码测试不采集桌面：

```powershell
cmake --build desktop/windows/build --config Release --target h264-smoke
.\desktop\windows\build\Release\h264-smoke.exe "$env:TEMP\svnc-h264-smoke.h264"
# 强制软件、严格硬件验收、模拟硬件在途故障：
.\desktop\windows\build\Release\h264-smoke.exe "$env:TEMP\svnc-software.h264" software
.\desktop\windows\build\Release\h264-smoke.exe "$env:TEMP\svnc-hardware.h264" hardware
.\desktop\windows\build\Release\h264-smoke.exe "$env:TEMP\svnc-fallback.h264" fallback
```

真实协议验收显式指定临时后端的 SSH 回环隧道：

```bash
SVNC_RFB_H264_ADDRESS=127.0.0.1:15906 \
SVNC_RFB_PASSWORD_FILE=/private/path/vnc-password.txt \
go test ./tests/integration -run TestLibVNCH264Backend -v -count=1
```

## 2026-11-19 g15 接入验收

Windows x64 Release 构建与 Go Agent 交叉构建通过。独立试用入口为 Agent `127.0.0.1:2225`、VNC `127.0.0.1:5905`；手动交互任务 `svnc-libvnc-managed-check`，没有登录自启触发器。连接客户端时使用 SSH 跳板 `g15`，VNC 目标仍填回环地址和测试端口，凭据沿用原部署。

已验证：

- 正常 VNC 认证，错误密码和未声明的 None 认证被拒绝。
- Raw 与 Tight/JPEG 画面、真实光标及 ExtendedClipboard 能力协商；分两段发送 SetEncodings 后正常读取画面。
- 临时窗口实际收到一次点击和一次 F8；独立只读实例发送相同事件未增加计数，也不能改写剪贴板。
- 中文文本经 ExtendedClipboard 双向传输；剪贴板变化期间，未完成握手的连接收不到剪贴板数据；不完整握手约 10 秒被回收。
- RFB 文件传输入口拒绝；同一 Agent 的 SFTP 可上传、下载、比较并删除测试文件。
- 后端被终止后 Agent 停止监听；手动重启恢复。停止计划任务后 Agent 和桌面后端均退出。
- Go 与客户端 race 测试、vet、前端测试、前端打包和 macOS MyGo app 构建通过；Agent 日志仍为逐行 JSON。

临时测试窗口、只读测试任务和测试文件已清理。此次为功能验收，没有建立相同画质下相对 UltraVNC 的性能优势结论。

2026-11-20，g15 的托管试用入口已迁到 `C:\ProgramData\SVNC\managed`，SFTP 根目录改为 `C:\Users\Administrator`。SSH 主机密钥及 VNC 密码已重新生成并同步到本机专用连接配置。原目录中的上传文件保留。

2026-11-20，g15 托管实例已更新为支持 VNC subsystem 的 Agent，SSH/SVNC 入口迁到 `127.0.0.1:5922`，后端仍为 `127.0.0.1:5905`。同一入口保留 SFTP 与受目标白名单限制的 `direct-tcpip`；普通 VNC 兼容入口保持关闭。任务仍为 `svnc-libvnc-managed-check`，当前用户交互运行，没有新增登录自启。

本机 g15 连接已迁为 SVNC subsystem 并重建客户端，实机已显示 2560×1440 桌面。通过 subsystem 和旧转发分别完成 RFB 认证拒绝、Raw 画面、光标、ExtendedClipboard 能力和未完成握手回收验收，SFTP 上传/下载内容一致并清理测试文件。此次没有操作远端键鼠或改写剪贴板。部署前程序、配置与任务 XML 备份在 `C:\ProgramData\SVNC\managed\backups\before-svnc-5922-20261120-103527`；主机密钥与 VNC 密码沿用原配置。
