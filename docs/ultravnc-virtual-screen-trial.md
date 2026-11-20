# UltraVNC 虚拟屏试验：g15，2026-11-19

## 目标与结果

目标是在同一 Windows 登录会话中，每个连接创建一块独立显示的虚拟屏。此模式仍共用 Windows 用户权限、输入焦点和剪贴板；本次不实现用户会话隔离或按 SSH 用户持久分配屏幕。

**g15 实机试验未通过，不能作为可用功能交付。** Windows 可以创建虚拟屏，但原型将收到的 `0,0 0×0` 消息当成选屏消息，等待后重试仍未显示独立屏幕。

后续复查发现：UltraVNC `rfbEncodingExtViewSize` 与 noVNC 标准的 `pseudoEncodingDesktopName` 均使用 `-307`。原型直接按该编号拦截，未区分桌面名称消息及其后续载荷。因此本次零尺寸错误混入了客户端协议适配问题，**不能据此认定 UltraVNC 后端持续返回了无效选屏区域**。原型未验证通过且已从当前客户端入口移除，用户决定先搁置虚拟屏。

## 环境与操作

- Windows 10 Pro 22H2，交互会话 2，UltraVNC 1.8.3.2。
- 使用该版本官方 ZIP 中的 `x64/UVncVirtualDisplay64`，Windows 验证目录文件和 DLL 的 Authenticode 签名有效，签署者为 `uvnc bvba`。
- `pnputil` 最初拒绝尚未信任的发布者。安装期间临时将目录签署者加入 TrustedPublisher，安装结束即移除本次新增的证书记录。驱动安装为 `oem126.inf`，版本 `17.6.9.32`。
- 临时启用 `EnableDriver=1`。客户端请求 1280×720 的 UltraVNC `dmExtendOnly` 模式。
- Windows 的交互会话屏幕枚举确认创建了 1280×720 的测试屏；连接重试期间出现了多块测试屏。
- 客户端持续把收到的 `-307` 消息判为零尺寸专属区域；因上述编码冲突，尚不能确认这些消息的真实含义。客户端断开后仍能枚举到测试屏。本次没有验证通过独立画面、键鼠或按连接回收，因此也没有继续实现按用户分配。

## 客户端原型

`client/frontend/ultravnc.js` 为固定的 noVNC 1.7.0 添加试验适配：

- 等待 ExtendedDesktopSize 能力消息后，仅发送一次 SetDesktopSize 请求，屏幕 flags 为 `3`（`dmExtendOnly`）。
- 处理 UltraVNC `rfbEncodingExtViewSize`（`0xFFFFFECD`，有符号值 `-307`），对有效区域进行本地视口裁剪，沿用 noVNC 的坐标换算。
- 零尺寸区域等待后续拓扑更新；20 秒未收到有效区域时断开并报告失败。
- 默认仍为共享当前桌面。原型仅用于后续诊断，不代表虚拟屏功能已验证通过。

前端语法检查、前端打包和 MyGo 应用构建通过；实机虚拟屏验收失败。共享桌面模式随后重新连接验证。

## 回退

已恢复试验前 `EnableDriver=0` 的 INI，停止测试进程、回收测试虚拟设备，并使用 `pnputil /delete-driver oem126.inf /uninstall` 成功卸载本次安装的驱动。Agent 与 UltraVNC 重新运行，SSH 和 VNC 保持回环监听。

配置备份保留在 g15 的 `%LOCALAPPDATA%\sshvnc-agent\x64\ultravnc.ini.before-virtual-screen-20261119-122041`，无需再次安装或重新信任发布者来恢复普通共享桌面。

## 后续定位

若恢复试验，应先修正 `-307` 消息的协商与载荷解析，再验证独立区域和设备回收。之后若仍有异常，再核对 UltraVNC 的新显示器名称识别、`devicenaamToPosMap` 更新过程及 DDEngine 的兼容性。

参考：[虚拟屏操作说明](https://uvnc.com/docs/documentation/134-ultravnc-virtual-displays.html)、[1.8.3.2 服务端处理](https://github.com/ultravnc/UltraVNC/blob/1.8.3.2/winvnc/winvnc/vncclient.cpp)、[虚拟显示器生命周期](https://github.com/ultravnc/UltraVNC/blob/1.8.3.2/winvnc/winvnc/VirtualDisplay.cpp)。
