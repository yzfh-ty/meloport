# 115 网盘、CD2 与 STRM 配置指南

Meloport 默认只把订阅歌曲下载到本地并补齐音频标签。115 交接、独立媒体监听、Navidrome 接入和歌单同步均默认关闭，已有地址配置不会自动开启这些功能。Navidrome 歌单同步支持本地音乐和 115 音频，不需要先开启网盘交接。

手动启用 115 交接后，Meloport 将完整音频移入 CD2 暂存目录，由 CD2 上传到 115。Meloport 核对远端文件后生成 `.strm`。115 交接适用于此实例的所有订阅任务，首次联调建议使用专用目录和曲目数量少的任务。

## 1. 准备目录并启用

1. 在 115 网盘新建专用子目录，打开目录并从地址中取得数字 `cid`。根目录 `0` 不可用。
2. 在 Meloport 控制台打开「115 与 STRM」→「配置 115 网盘」。勾选启用，填入目录 ID，确认下表中的路径与地址，然后保存并重启 Meloport。
3. 在 CD2 配置单向上传，监控 `staging_dir`，将该目录的**内容**上传到选定的 115 目录。保留平台和歌单子目录，使远端布局为 `<目标目录>/<平台>/<歌单名--任务ID>/<音频文件>`。旧的 `<任务ID>/<音频文件>` 记录仍可核对和修复。如需释放暂存空间，让 CD2 在确认上传成功后清理源文件。

| 菜单页面 | 网页设置 | YAML 字段 | 默认值与用途 |
| --- | --- | --- | --- |
| 115 与 STRM | 115 网盘交接与 STRM | `enabled` | `false`，关闭时保留本地下载流程 |
| 115 与 STRM | 115 目标目录 ID | `target_cid` | 必填，CD2 的远端上传目标 |
| 115 与 STRM | CD2 暂存目录 | `staging_dir` | `data/115-staging`，与下载根目录、任务工作目录位于同一文件系统，供 CD2 监控 |
| 115 与 STRM | STRM 导出目录 | `strm_dir` | `data/strm`，供支持音频 STRM 的播放器读取 |
| Navidrome | Navidrome 接入 | `navidrome_enabled` | `false`，仅填写地址不会启用反代 |
| Navidrome | Navidrome 服务地址 | `navidrome_url` | 启用 Navidrome 时必填 |
| Navidrome | Navidrome 中的音乐目录 | `navidrome_mount_root` | Navidrome 运行环境看到的音乐目录绝对路径 |
| Navidrome | 同步创建 Navidrome 歌单 | `navidrome_playlist_sync` | `false`，将普通歌单订阅和排行榜同步到 Navidrome；需要接入账号 |
| Navidrome | 独立媒体端口 | `media_enabled` | `false`，STRM 和 Navidrome 共用的播放入口 |
| Navidrome | 播放器访问地址 | `media_base_url` | `http://127.0.0.1:8081`，写入 STRM。跨设备使用播放器能访问的 HTTPS 地址 |

本地相对路径从配置文件所在目录解析。暂存目录、STRM 目录、下载工作目录和数据库应当各自独立。Meloport 从工作目录向 CD2 暂存目录原子移动完整音频，因此需要同卷路径。YAML 配置字段和默认值见 [配置示例](../config.example.yaml)。

只启用 115 交接不会启动媒体监听，也不会接入 Navidrome。只启用 Navidrome 不会开启 CD2 交接，订阅歌曲继续下载到本地。各开关及监听地址修改后均需重启服务生效。

「115 与 STRM」和「Navidrome」各自保存当前页的设置，切换菜单会保留未保存的草稿。保存一页不会同时提交另一页的草稿。Navidrome 歌单同步与 115 开关独立；关闭 115 交接后，后续同步使用本地下载目录。

## 2. 扫码登录并检查连接

1. 重启后返回「115 与 STRM」，点击「扫码登录」。用手机 115 App 扫码，在手机上确认授权。
2. 页面授权成功后，点击「检查连接」，确认当前账号能读取目标目录。二维码超时可重新获取。

扫码得到的短期 Access Token 只保留在 Meloport 进程内。令牌过期或服务重启后，重新扫码。网页没有令牌输入框，也没有自动续期。应用 ID 默认 `100197303`，需要更换时设置 `MELOPORT_115_CLIENT_ID` 并重启。扫码授权优先于环境变量，退出扫码授权后恢复使用启动时已配置的环境变量。

如果启用了 115 交接但尚未授权，网页仍能启动。需要交接的新音频任务会提示先登录。仅使用命令行时，可按本文最后的环境变量方式授权。

## 3. 核对上传并生成 STRM

1. 在「任务概览」运行订阅任务，等待 CD2 上传完成。
2. 回到「115 与 STRM」点击「核对并生成」。服务也会按核对间隔自动运行。远端文件名、大小和 SHA-1 唯一匹配后，才会确认该音频。
3. 从 `<strm_dir>/<平台>/<歌单名--任务ID>/<音频文件>.strm` 找到导出文件。若 STRM 被删除或内容被修改，再次核对即可修复。匹配记录已存在时，修复对应 STRM 不需要重新下载音频。

| 页面状态 | 含义 |
| --- | --- |
| 待远端确认 | Meloport 已登记交接，尚未通过远端文件校验 |
| 已确认音频 | 远端文件已校验，包含已生成 STRM 的记录 |
| 已生成 STRM | 本地导出文件存在，且内容与该记录及当前播放地址一致 |

详情最多显示 100 条，统计涵盖当前目标目录的全部登记记录。已经交给 CD2 的歌曲不会因本地暂存文件清理而重复下载。网页「音频文件」检查的是下载工作目录，出现本地缺失时可在本页核对远端状态。

## 4. 配置播放器

**支持音频 STRM 的播放器**读取导出文件中的稳定媒体地址。使用前，在独立的「Navidrome」菜单页的「媒体入口」开启「独立媒体端口」，填写播放器访问地址 `media_base_url`，保存并重启。也可从「115 与 STRM」的「配置媒体入口」链接进入。媒体默认监听 `127.0.0.1:8081`，需要更换时通过 `MELOPORT_MEDIA_LISTEN` 配置；直接访问时，播放器地址中的端口应与监听端口一致。跨设备访问时，填写播放器能访问的 HTTPS 反代地址，并将反代上游指向配置的媒体监听地址。只播放 STRM 不需要开启 Navidrome 接入。

播放器请求该地址时，Meloport 按请求的 User-Agent 获取 115 临时直链并返回 HTTP `302`。反代应保留 User-Agent，并将 302 返回给播放器。如果配置了地址路径前缀，反代应去掉前缀后再转发。持有 STRM 链接即可请求对应音频，请保护导出目录，并在反代访问日志中隐藏媒体路径。关闭独立媒体端口后，已生成的 STRM 会保留，但需重新开启端口才能通过 Meloport 播放。

**Navidrome** 可以单独接入本地下载模式，也可以搭配 115。Navidrome 扫描本地音乐目录，或 CD2 挂载的 **115 真实音频目录**，`.strm` 文件供支持该格式的其他播放器使用。

1. 打开侧边栏的「Navidrome」菜单，分别开启「Navidrome 接入」和「独立媒体端口」。填写播放器地址、`navidrome_url`，以及 Navidrome 运行环境看到的音乐目录绝对路径 `navidrome_mount_root`。保存后重启。
2. 将 Subsonic 客户端连接到 Meloport 媒体地址，使用 Navidrome 的账号和原生认证。本地模式的音频请求由 Navidrome 处理；无需开启 115，也不会将本地音频移入 CD2 目录。请勿将 Meloport 加入 Navidrome 的外部鉴权代理信任列表。
3. 如果需要 115 的 302 直链播放，另行启用 115 交接、完成远端核对，并让 Navidrome 扫描 CD2 挂载的真实音频。在对应播放器设置中开启 **Report Real Path**，关闭服务端转码和码率限制。
4. 如需自动创建或更新歌单，先在服务进程环境设置 `MELOPORT_NAVIDROME_USERNAME` 和 `MELOPORT_NAVIDROME_PASSWORD`，再启用「同步创建 Navidrome 歌单」，保存并重启。这是 Navidrome 的账号，Meloport 用它调用歌单写入接口。普通歌单订阅和排行榜均支持；本地音乐模式无需开启 115。远程服务必须使用 HTTPS，本机可用 HTTP。密码只从 Meloport 进程环境读取。

本地模式下，「Navidrome 中的音乐目录」应填写**下载根目录在 Navidrome 运行环境中对应的路径**。例如 Meloport 下载根目录为 `E:\music`，任务文件为 `E:\music\favorites\netease-1.flac`；该根目录映射到 Navidrome 的 `/music` 时，填写 `/music`，歌曲路径应为 `/music/favorites/netease-1.flac`。任务的自定义输出目录不在下载根目录内时，无法通过这个根目录映射，发布会提示路径不匹配。115 模式则填写 CD2 挂载的 115 目标目录，歌曲相对路径以数据库中保存的交接记录为准。

每次完整任务同步结束后，Meloport 按文件路径、歌名、歌手和可用的专辑信息匹配 Navidrome 已入库的曲目。所有歌曲匹配成功才创建或更新；Navidrome 尚未扫描完成时保留原歌单，在日志中记录提示，下次任务同步再试。全部歌曲已下载、本次全部跳过时，也会更新歌单。115 的「核对并生成」不会触发歌单发布。

歌单名为「平台 - 歌单名」，例如 `网易云音乐 - 收藏`，不附加任务 ID。已有唯一的旧名 `网易云音乐 - 收藏 [favorites]` 时，会在下一次成功发布时改名并保留其 Navidrome 歌单 ID。发现多个同名歌单时停止更新，避免选错。更新按当前订阅的曲目顺序替换歌单内容，已退出订阅的歌曲以及手动加到该歌单的歌曲会从歌单中移除，音频文件仍保留。

同一任务按平台歌曲 ID 去重，重复成员只处理一次，完整的已下载文件会跳过。不同任务使用独立输出目录，同一平台的同一首歌可能各下载一份；跨平台或不同歌曲 ID 不会按歌名合并。创建、更新或改名 Navidrome 歌单本身不会解析或下载音频。

经过 Meloport 的 `/rest/stream` 或 `/rest/stream.view` 请求，在原生鉴权成功、有真实路径且能匹配远端文件时尝试 302。显式请求转码或限码率、无法匹配、取直链失败以及 HLS 等其他端点继续由 Navidrome 处理。直接连接 Navidrome 时，Navidrome 从 CD2 挂载读取音频。

## 部署环境变量

应用 ID、授权变量名、核对参数、媒体监听地址和 Navidrome 同步账号由服务端配置，网页只保留接入开关、目录和播放器地址。系统设置页只显示解析密钥是否已配置；ChKSz 密钥仍使用 `CHKSZ_API_KEY`，平台 Cookie 仍通过对应环境变量提供。

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `MELOPORT_115_CLIENT_ID` | `100197303` | 115 扫码使用的数字应用 ID |
| `MELOPORT_115_ACCESS_TOKEN` | 无 | 可选的短期授权令牌；也可在网页扫码 |
| `MELOPORT_115_POLL_INTERVAL` | `1m` | 自动核对间隔，范围 `15s`–`24h` |
| `MELOPORT_115_TIMEOUT` | `15s` | 115 和 Navidrome 请求超时，范围 `1s`–`1m` |
| `MELOPORT_MEDIA_LISTEN` | `127.0.0.1:8081` | 独立媒体监听地址；需在网页启用媒体端口 |
| `MELOPORT_NAVIDROME_USERNAME` | 无 | 创建订阅歌单和榜单歌单使用的 Navidrome 账号 |
| `MELOPORT_NAVIDROME_PASSWORD` | 无 | 上述账号的密码，仅从进程环境读取 |

在启动 Meloport 的同一个 PowerShell 窗口中配置，例如：

```powershell
$env:MELOPORT_MEDIA_LISTEN = '127.0.0.1:18081'
$env:MELOPORT_115_POLL_INTERVAL = '2m'
$env:MELOPORT_NAVIDROME_USERNAME = 'meloport'
$navidromePassword = Read-Host 'Navidrome 账号密码' -AsSecureString
$env:MELOPORT_NAVIDROME_PASSWORD = [System.Net.NetworkCredential]::new('', $navidromePassword).Password
Remove-Variable navidromePassword
.\bin\meloport.exe serve -config config.yaml
```

以上非空的部署环境变量优先于已有 YAML 中的 `client_id`、`poll_interval`、`timeout`、`media_listen` 和 `navidrome_username`；未提供时继续使用原配置或默认值。为兼容现有部署，YAML 中的 `access_token_env` 和 `navidrome_password_env` 仍可指定旧的令牌、密码变量名。网页保存其他设置时会保留这些旧字段及注释，并且不会把环境变量值写入 YAML。修改进程环境后需重启；由服务管理器启动时，应在其运行环境中设置。

## 常见问题

| 现象 | 排查方式 |
| --- | --- |
| 修改了媒体端口但没有监听 | 确认已开启「独立媒体端口」并重启。只有填写地址不会启动监听。 |
| 填写 Navidrome 地址后仍未接入 | 确认已开启「Navidrome 接入」和「独立媒体端口」，保存后重启。 |
| 网页显示「重启后应用」或没有扫码按钮 | 确认已勾选启用并保存，终止旧进程后重新 `serve`，再刷新页面。 |
| 扫码已确认，但连接检查失败 | 核对手机 115 账号与 CD2 使用的网盘目录。确认填入的是该专用目录的 CID，而且当前账号有读取权限。令牌过期时重新授权。 |
| 一直「待远端确认」 | 检查 CD2 失败队列和 115 实际目录结构。核对平台、歌单子目录是否保留，文件是否完整。暂存文件早于上传完成被清理时，从备份或 CD2 恢复对应文件。 |
| 已确认但未生成 STRM | 检查导出目录权限、同名文件类型和服务日志，再点「核对并生成」。 |
| 其他设备打不开 STRM | 确认播放器支持音频 STRM，地址在播放器所在网络可达，HTTPS 反代指向媒体端口。修改播放地址并重启后重新核对，使导出文件使用新地址。 |
| Navidrome 能播放但没有 302 | 核对客户端连接的实际地址、Report Real Path、挂载目录绝对路径和 115 映射，确认此次播放采用原文件格式。 |
| 下载已完成但 Navidrome 歌单未创建 | 确认已开启歌单同步、提供 Navidrome 账号环境变量，且 Navidrome 已扫描真实音频。核对音乐根目录映射，再运行一次订阅任务；查看日志中的歌单同步提示。 |

## 命令行与验收范围

可用 [OpenList 授权页](https://api.oplist.org/115cloud/requests?driver_txt=115cloud_go&server_use=true) 的 115 网盘验证登录取得专用于 Meloport 的短期 Access Token。在运行命令的进程环境中设置 `MELOPORT_115_ACCESS_TOKEN`，或使用 `access_token_env` 指定的名称。YAML 只填写变量名。

```powershell
.\bin\meloport.exe cloud-check -config config.yaml
.\bin\meloport.exe cloud-check -config config.yaml -downurl
.\bin\meloport.exe cloud-reconcile -config config.yaml
```

`cloud-check` 验证授权和目录读取。`-downurl` 另检查一个已完成文件能否取得链接，命令只打印结果。`cloud-reconcile` 写入数据库和 STRM，使用同一数据库时先停止 `serve`。服务已运行时，在网页进行核对。

目前已用 115 正式接口验证二维码申请和未扫码时的等待轮询。本地模拟测试覆盖扫码状态、文件核对、导出和 Subsonic 回源。真实账号扫码换令牌、CD2 上传、真实 Navidrome 曲库扫描，以及客户端首播、拖动和续播仍待联调。
