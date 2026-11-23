# 控制台登录

Meloport 使用一个管理员密钥登录 Web 控制台并访问 `/api/` 管理接口。登录页只需输入密钥，无需用户名或注册。管理员密钥与 ChKSz 音频解析密钥各自独立。

## 设置管理员密钥

在启动服务的同一个进程环境设置 `MELOPORT_ADMIN_PASSWORD`，密码长度为 12–1024 个 UTF-8 字节。密码可以包含空格，登录时保留原样。开发测试也可使用下面的本地 YAML 配置方式。

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

开发环境可在本地 `config.yaml` 中配置测试密码，然后直接运行 `start-test.cmd` 或 `scripts/dev.ps1 -Action dev`：

```yaml
server:
  listen: "127.0.0.1:8080"
  admin_password: "YOUR_LOCAL_TEST_PASSWORD"
```

密码长度同样为 12–1024 个 UTF-8 字节。`config.yaml` 已被 Git 忽略。长度有效的 `MELOPORT_ADMIN_PASSWORD` 优先于 YAML；环境变量缺失或长度无效时使用 YAML 中的密码。若需替换原有有效的环境变量密码，应先清除该变量。由服务管理器启动时，也可在其运行环境提供密码。

如果环境变量和 YAML 均未提供有效密码，服务仍可启动，但控制台和管理接口不会开放。YAML 密码长度不合法时，配置校验失败。修改密码后需要重启服务才能生效。

## 登录与退出

打开控制台后，只需输入服务端配置的管理员密钥。登录成功后可使用订阅、检索、音频、115 和设置等管理功能。环境变量名称继续使用 `MELOPORT_ADMIN_PASSWORD`，本地开发配置继续使用 `server.admin_password`。

会话保留 12 小时，刷新页面无需再次登录。侧边栏底部的“退出登录”会立即撤销当前会话。服务重启后所有登录失效。支持同一个管理员在多个浏览器登录，最多保留 16 个会话；超过时撤销最早到期的会话。

连续 5 次凭据错误后，会暂停登录尝试 30 秒。密码与会话均不写入 SQLite，管理接口不会返回密码；网页编辑其他设置时会保留本地 YAML 中已有的密码字段。浏览器通过 HttpOnly、SameSite=Strict 的 Cookie 保存会话标识；不会把密码或令牌写入 localStorage。

管理监听仍只允许本机 loopback 访问，并保留同源写入保护。登录不会改动 CLI 同步、定时任务、`/healthz`、`/readyz`，也不替代播放器的鉴权。独立媒体端口默认关闭；在独立的「Navidrome」菜单页的「媒体入口」手动开启并重启后，STRM 播放地址与 Subsonic 原生认证走该端口，监听地址通过 `MELOPORT_MEDIA_LISTEN` 配置，默认 `127.0.0.1:8081`。
