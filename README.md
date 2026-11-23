# Meloport

Meloport 按计划同步网易云、QQ 音乐和酷狗的歌单或排行榜，只下载缺少的歌曲，保留音频原有格式。网页可管理订阅、检索歌曲、查看同步结果和补齐音频标签。可选通过 CD2 上传到 115 网盘并导出 STRM。

[配置示例](config.example.yaml) · [115 / STRM / Navidrome 配置指南](docs/115-strm.md) · [第三方组件与许可证](docs/THIRD_PARTY.md)

## 快速启动

在 Windows 上安装 Go 1.26 或更新版本，从项目根目录运行以下 PowerShell 命令。

```powershell
.\scripts\dev.ps1 -Action build
$adminPassword = Read-Host '设置控制台管理员密码（至少 12 字节）' -AsSecureString
$env:MELOPORT_ADMIN_PASSWORD = [System.Net.NetworkCredential]::new('', $adminPassword).Password
Remove-Variable adminPassword
.\bin\meloport.exe serve -config config.yaml
```

打开 [本地控制台](http://127.0.0.1:8080/)。首次运行会在缺少 `config.yaml` 时创建空任务配置。已有配置会继续使用。修改管理端口时，以启动日志为准。按 `Ctrl+C` 停止服务。

登录页只需输入上面设置的管理员密钥，无需用户名。开发测试也可在本地 YAML 中设置 `server.admin_password`（12–1024 字节）；长度有效的 `MELOPORT_ADMIN_PASSWORD` 优先，缺失或长度无效时使用 YAML 中的密钥。该密钥与 ChKSz 解析密钥独立。没有有效管理员密钥时控制台保持锁定。密钥不写入数据库或管理接口响应。登录有效期为 12 小时，可在侧边栏底部退出；服务重启后重新登录。Linux/WSL 配置方式和会话说明见 [控制台登录](docs/login.md)。

## 添加订阅和下载

1. 在「系统设置」设置下载根目录、默认同步时间和音质。每个订阅可以覆盖默认规则。默认下载到 `data/music/<任务ID>/`。
2. 在「任务概览」点击「添加订阅」，选择平台并粘贴完整歌单链接或歌单 ID。保持「启用自动同步」勾选，点击「添加并开始下载」即可自动排队进行首次同步。也可从「排行榜」直接订阅榜单。
3. 在「运行记录」查看结果，后续按计划增量同步。需要重试时点击任务的「同步」按钮。取消勾选自动同步时只保存订阅；下载队列暂停或已满时也会保存订阅，并提示首次下载尚未排队。

下载前会先匹配已登记的本地歌曲。同一平台、同一歌曲 ID，且可用的版本信息一致时，会校验文件大小和 SHA-256，从本地复制到新订阅的目录，不再调用音源解析接口或下载音频。复用计入「跳过」；已下载歌曲继续保留现有音质，音质规则只影响缺失文件。歌曲已退出其他歌单或原订阅已删除时，保留的记录和完整文件仍可复用。跨平台、不同歌曲 ID，以及仅歌名相同的文件不会合并；未经 Meloport 登记的外部音频不会按标题自动匹配。远端移除歌曲或删除任务时，音频保留。

启动控制台、原生搜索和预览无需 Key。下载缺少的音频需要个人 ChKSz API Key，可在 [ChKSz 账户页面](https://api.chksz.com/login) 获取。开发环境可直接写入被 Git 忽略的 `config.yaml` 的 `api.key`，然后重启服务。有效的密钥环境变量优先；环境变量缺失或格式无效时回退到 YAML。也可在启动服务的同一个 PowerShell 窗口中设置环境变量：

```powershell
$secureKey = Read-Host 'ChKSz API Key' -AsSecureString
$env:CHKSZ_API_KEY = [System.Net.NetworkCredential]::new('', $secureKey).Password
Remove-Variable secureKey
.\bin\meloport.exe serve -config config.yaml
```

歌单、搜索和元数据优先直连音乐平台。开启「直连失败时允许 ChKSz 回退」后，支持回退的元数据请求会使用 ChKSz 额度。需要平台登录时，可通过配置示例中对应的环境变量提供 Cookie。实际 Key 只放进程环境或本地配置，Cookie 只放进程环境；管理接口不返回密钥。

## 保存配置与管理音频

网页保存任务和「系统设置」后会更新当前服务。手动编辑 YAML 或保存「115 与 STRM」设置后，需要重启服务。配置内的相对路径从配置文件所在目录解析，任务的相对保存路径以下载根目录为准。可参考 [配置示例](config.example.yaml) 并检查修改结果。

系统设置页保留填写的目录值，说明文案使用通用描述，不额外显示服务端展开后的绝对路径。

```powershell
.\bin\meloport.exe validate -config config.yaml
```

已下载的文件可在「音频文件」筛选缺失的歌名、歌手、专辑、封面和歌词，然后点击「重试补全」。Meloport 会保留现有标签和音频内容。使用 `retag` 可批量检查和补标签，执行前请停止使用同一个数据库的服务。

```powershell
.\bin\meloport.exe retag -config config.yaml -task favorites
.\bin\meloport.exe status -config config.yaml -limit 20
```

## 115 网盘、CD2 与 STRM

默认仅将订阅歌单或榜单的歌曲下载到本地。115/CD2 交接、独立媒体端口、Navidrome 接入和 Navidrome 歌单同步均默认关闭，填写地址不会自动开启功能。

需要网盘时，在「115 与 STRM」开启「115 网盘交接与 STRM」，填写目标目录和 CD2 暂存目录，保存并重启，再用手机 115 App 扫码。此时完整音频会移入 CD2 监控的暂存目录。CD2 上传完成后，Meloport 定时核对远端文件并生成 STRM，也可以点击「核对并生成」。

Navidrome 在侧边栏的独立菜单中配置，包含接入开关、服务地址、音乐目录、歌单同步，以及 STRM 共用的媒体入口。开启「独立媒体端口」后，默认监听 `127.0.0.1:8081`；需要更换时设置 `MELOPORT_MEDIA_LISTEN`。开启「Navidrome 接入」并填写服务地址与音乐目录后，可反代 Navidrome；不启用 115 时，订阅仍保存在本地。提供 Navidrome 账号并开启「同步创建 Navidrome 歌单」后，普通歌单订阅和排行榜都可创建为 Navidrome 歌单，本地模式也可使用。名称为「平台 - 歌单名」，不附加任务 ID。歌曲全部入库后，下一次完整任务同步会创建或更新歌单；此操作不会另行下载音频。同一任务中的重复平台歌曲 ID 会合并，不同订阅的同一首歌优先复用完整本地音频，各自保留输出目录。需要 STRM 或 115 的 302 直链播放时，还需开启 115 交接并完成远端核对。两个页面各自保存设置。应用 ID、核对间隔、请求超时及 Navidrome 同步账号通过[部署环境变量](docs/115-strm.md#部署环境变量)配置。接入设置和环境变量修改后需重启生效。目录配置和排错见 [115 / STRM / Navidrome 配置指南](docs/115-strm.md)。

## 本地开发

双击 `start-test.cmd` 或运行 `scripts/dev.ps1 -Action dev -Config config.yaml`。在本地 `config.yaml` 的 `server.admin_password` 中填写测试密钥后，可直接启动并输入该密钥登录。改动网页资源后刷新浏览器，改动 Go 代码后重启开发服务。

```powershell
.\scripts\dev.ps1 -Action test
.\scripts\dev.ps1 -Action build
```

本地测试覆盖增量同步、标签、网页配置、115 核对导出和 Subsonic 反代。已使用 115 正式接口验证二维码申请和未扫码轮询。真实账号授权、CD2 上传和 Navidrome 客户端实际播放仍待联调。
