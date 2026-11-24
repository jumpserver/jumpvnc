# JumpVNC 开发指南

本指南说明源码构建、测试和后端开发入口。下载安装与连接步骤见 [项目首页](../README.md)。以下命令默认从仓库根目录执行。

## 服务端

需要 Go 1.26.9 或更新版本。

```bash
mkdir -p bin
go build -o bin/jumpvnc ./cmd/server
```

Windows 下构建 Agent：

```powershell
go build -o bin\jumpvnc.exe .\cmd\server
```

Agent 与桌面后端分别构建，根据平台选择后端：

- [Windows LibVNCServer + DXGI 后端](../desktop/windows/README.md)：依赖准备、编译、系统服务部署与真实 RFB 验收。
- [Linux LibVNCServer + X11 后端](../desktop/linux/README.md)：CMake 构建、运行依赖及隔离 Xvfb 测试。

构建后按 [快速开始](../README.md#快速开始) 准备配置、密钥和密码。`configs/linux.example.yaml` 与 `configs/windows.service.example.yaml` 使用 LibVNCServer 后端。

### 可选的 TigerVNC 后端

TigerVNC 仍是受支持的可选后端。`configs/server.example.yaml` 使用该后端，不是 Linux 打包后端的配置示例。需要 OpenSSH 客户端、TigerVNC 的桌面共享和密码工具；Debian 对应 `openssh-client`、`tigervnc-scraping-server` 和 `tigervnc-tools`。

使用该配置时，通过 `vncpasswd` 生成配置中引用的 `vnc.passwd`。LibVNCServer 使用的是 1–8 字节可打印 ASCII 明文密码文件 `vnc-password.txt`，两种密码文件格式不能混用。

## 桌面客户端

支持 macOS、Windows、Linux，需要 Go 1.27.3 或更新版本，以及 Node.js 和 npm。客户端使用独立的 Go 模块。SSH、跳板机与 SFTP 均由 Go 实现，无需本机 `ssh` 命令。

```bash
cd client
npm --prefix frontend ci
go tool mygo build -skip-dmg
```

Apple Silicon 构建结果为 `client/build/darwin-arm64/JumpVNC.app`。在 `client` 目录构建 Apple Silicon / Intel 通用版本：

```bash
go tool mygo build -platform darwin/universal -skip-dmg
```

Windows 和 Linux 构建：

```bash
go tool mygo build -platform windows/amd64
go tool mygo build -platform linux/amd64,linux/arm64
```

Windows 构建输出可执行文件与 NSIS 安装程序；Linux 输出应用归档、用户安装脚本、Debian / RPM 包及 PKGBUILD。跨平台只构建 Windows 可执行文件时可加 `-skip-installer`。运行依赖、安装方式和平台功能限制见 [客户端文档](../client/README.md#安装)。CI 在 Windows amd64、Linux amd64/arm64 上分别测试并打包；发布流程要求所有新增产物及校验文件齐全。

客户端实现、配置与性能指标见 [客户端文档](../client/README.md)。`client-libvnc` 是独立实验客户端，未替换正式 MyGo / noVNC 客户端。

## 测试

在仓库根目录检查服务端：

```bash
go test -race ./...
go vet ./...
```

客户端需单独安装前端依赖并检查：

```bash
cd client
npm --prefix frontend ci
npm --prefix frontend test
go test -race ./...
go vet ./...
```

真实桌面集成测试需要显式配置测试环境，具体命令见各平台后端文档。构建矩阵、打包与依赖版本见 [CI 工作流](../.github/workflows/build.yml)。

## 协议与集成

- [SVNC 接入协议](protocol.md)：SSH 子系统、桌面与文件权限、兼容方式及 JumpServer / Guacamole 集成约定；当前为 v0.1 草案。
- [`.svnc` 连接文件格式](connection-file.md)：连接配置的导入、导出与共享。

## Windows 安装程序

`deployments/windows/installer/jumpvnc.iss` 使用 Inno Setup 6 将 Windows 服务端目录打包为安装程序，自动准备 Microsoft Visual C++ 运行库、初始化随机凭据、注册服务和托盘、添加程序防火墙规则。编译前由 `build.ps1` 核验运行库安装包的 Microsoft 数字签名。

CI 在完成后端构建及打包后执行：

```powershell
.\deployments\windows\installer\build.ps1 -Version 0.1.2
.\deployments\windows\installer\test.ps1 -Installer .\dist\jumpvnc-server-windows-amd64-setup.exe
```

安装验收仅在一次性 Windows 构建环境运行，覆盖首次安装、升级保留凭据、管理窗口复制动作、截图和卸载保留配置。`/NoStartService=1` 仅用于该验收环境；正常安装自动启动服务。初始化不会覆盖已有配置、授权公钥、主机密钥或密码；连接密钥与主机密钥分别生成，私钥和配置只允许管理员及 SYSTEM 读取。
