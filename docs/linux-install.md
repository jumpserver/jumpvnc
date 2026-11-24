# Linux 安装与管理

下载对应架构的 JumpVNC Server tar.gz，解压后在已登录的 X11 桌面终端运行 `bash install.sh`。以当前桌面用户运行，脚本会按需请求 sudo 权限，不需要 Go、Node.js 或编译工具。

## 安装行为

- 程序安装到 `/opt/jumpvnc`，可在安装后删除下载包。
- 配置与凭据位于 `/var/lib/jumpvnc/<用户名>`，目录权限 `0700`、凭据权限 `0600`，位于 SFTP 文件根目录之外。
- 自动生成独立的 SSH 主机密钥、客户端连接密钥和随机 VNC 密码。默认 SSH 用户名 `vncuser`、端口 `5922`；文件根目录为当前用户的主目录。
- 注册 `jumpvnc.service` 用户服务及桌面登录自启项，自动导入每次登录的 X11 环境；服务异常退出时自动重启。
- 再次运行新下载包中的安装脚本完成升级，保留原配置、密钥、密码及授权列表，自动迁移旧版默认配置位置。

## 系统要求

当前官方服务端包基于 Debian 13 构建，提供 amd64 和 arm64。需要 systemd 用户服务和 X11；不支持 Wayland。缺少运行库时，安装脚本会在 Debian 13 上通过 apt 自动安装。其他发行版需要提供兼容的 glibc、FFmpeg 及 X11 运行库，脚本会在不兼容时停止，避免安装无法运行的程序。

需要按访问来源放行 TCP `5922` 的防火墙与安全组。后端 VNC 仅监听回环地址，不需要开放 `5900`。

暂时没有已登录的图形会话时，可运行 `bash install.sh --no-start`，安装后等待下次 X11 桌面登录自动启动。此模式不自行创建桌面；无显示器机器的独立桌面部署见 [Xvfb 说明](../deployments/linux/README.md)。

## 查看连接信息

安装结束会显示连接地址、用户名、VNC 密码、主机指纹及连接私钥文件路径。之后可以再次查看：

```bash
bash /opt/jumpvnc/install.sh --info
```

默认连接私钥文件为 `/var/lib/jumpvnc/<用户名>/client_ed25519_key`。将私钥保存到客户端的受限目录，在 Viewer 中选择 SVNC 并填写“SSH 私钥文件”路径；也可修改 `authorized_keys`，改用自己已有的密钥。

## 服务与配置

```bash
systemctl --user status jumpvnc.service
systemctl --user restart jumpvnc.service
systemctl --user stop jumpvnc.service
journalctl --user -u jumpvnc.service -n 50
```

配置文件为 `/var/lib/jumpvnc/<用户名>/config.yaml`，修改后重启服务生效。SFTP 的 `mode` 可设为 `read_write`、`read_only`、`write_only` 或 `disabled`，`root` 指定文件根目录。不要把配置、密钥、密码或程序所在目录设为 SFTP 根目录。

## 移除当前用户的自启

```bash
systemctl --user stop jumpvnc.service
rm "${XDG_CONFIG_HOME:-$HOME/.config}/autostart/jumpvnc.desktop"
rm "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/jumpvnc.service"
systemctl --user daemon-reload
```

程序由系统安装，可能由多个桌面用户共用；上述步骤保留程序、配置、凭据及用户文件。
