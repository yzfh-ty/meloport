# 控制台登录

Meloport 只有一个管理员账号 `admin`，用于登录 Web 控制台和访问 `/api/` 管理接口。无需注册、用户数据库或角色配置。

## 设置密码

在启动服务的同一个进程环境设置 `MELOPORT_ADMIN_PASSWORD`，密码长度为 12–1024 字节。密码可以包含空格，登录时保留原样。不要将真实密码写入 YAML、脚本、Git 或命令历史。

PowerShell：

```powershell
$adminPassword = Read-Host '控制台管理员密码' -AsSecureString
$env:MELOPORT_ADMIN_PASSWORD = [System.Net.NetworkCredential]::new('', $adminPassword).Password
Remove-Variable adminPassword
.\bin\meloport.exe serve -config config.yaml
```

Linux / WSL Bash：

```bash
IFS= read -rs -p '控制台管理员密码: ' MELOPORT_ADMIN_PASSWORD
printf '\n'
export MELOPORT_ADMIN_PASSWORD
./meloport serve -config config.yaml
```

使用 `scripts/dev.ps1 -Action dev` 时同样在启动前设置环境变量。终端重开后需重新提供密码；由服务管理器启动时，在其运行环境提供该变量。

如果未配置密码、少于 12 字节或超过 1024 字节，服务仍可启动，但控制台和管理接口不会开放。登录页面会提示先配置密码并重启。修改环境中的密码需要重启服务才能生效。

## 登录与退出

打开控制台后输入密码。登录成功后可使用订阅、检索、音频、115 和设置等管理功能。账号固定为 `admin`。

会话保留 12 小时，刷新页面无需再次登录。页面底部的“退出登录”会立即撤销当前会话。服务重启后所有登录失效。支持同一个管理员在多个浏览器登录，最多保留 16 个会话；超过时撤销最早到期的会话。

连续 5 次凭据错误后，会暂停登录尝试 30 秒。密码与会话均不写入 SQLite 或配置文件。浏览器通过 HttpOnly、SameSite=Strict 的 Cookie 保存会话标识；不会把密码或令牌写入 localStorage。

管理监听仍只允许本机 loopback 访问，并保留同源写入保护。登录不会改动 CLI 同步、定时任务、`/healthz`、`/readyz`，也不替代播放器的鉴权：STRM 播放地址与 Subsonic 原生认证继续走独立的媒体监听端口。
