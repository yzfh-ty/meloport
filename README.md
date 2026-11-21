# Meloport

歌单订阅下载服务：定时读取歌单，只下载本地缺少的歌曲，保留原始音频格式。使用 Go 与 SQLite，在本机运行，不需要单独安装数据库服务。

**歌单、搜索和歌曲元数据优先直连音乐平台；ChKSz 主要用于缺失歌曲的音频解析。** 当前正式接入网易云、QQ 音乐、酷狗，范围与 ChKSz 支持的平台一致。其他平台只保留 `Source` 扩展接口，没有注册虚构实现。开发阶段暂不打包 Docker。

## 本机启动与网页管理

需要 Go 1.26 或更高版本。开发脚本将缓存放在项目 `.cache`，普通构建使用 `CGO_ENABLED=0`，执行结束恢复原环境变量。

```powershell
.\scripts\dev.ps1 -Action build
.\bin\meloport.exe serve -config config.yaml
```

修改后需要手动测试时，双击项目根目录的 **`start-test.cmd`** 即可启动开发服务，也可以在终端运行：

```powershell
.\start-test.cmd
# 使用其他配置文件（相对路径以项目根目录为基准）：
.\start-test.cmd -Config "tmp\manual-test.yaml"
```

脚本复用下面的开发命令，每次启动自动编译 Go，使用 `config.yaml` 和当前终端的环境变量；配置不存在时由服务创建。启动后打开 **http://127.0.0.1:8080/**；如果修改了 `server.listen`，请使用日志中的实际地址。测试结束按 `Ctrl+C` 停止服务；若命令提示符询问是否终止批处理，输入 `Y`。启动失败时窗口会保留错误提示。该入口会运行配置中的定时任务，请留意已有订阅和 `run_on_start` 设置。

也可以直接在 PowerShell 中运行：

```powershell
.\scripts\dev.ps1 -Action dev -Config config.yaml
```

该命令先编译 `bin/meloport-dev.exe`，再在当前窗口直接运行服务，并从 `internal/webui/assets` 按请求读取 `index.html`、`app.css` 和 `app.js`。修改这三个文件后刷新浏览器即可看到结果；修改 Go 后停止并重新运行命令。开发资源目录仅在显式 `dev` 命令中启用，生产 `serve` 仍使用嵌入资源。

打开 **http://127.0.0.1:8080/**。首次 `serve` 如果找不到配置文件，会创建仅含空任务列表的本地配置；**无需 Key 即可启动控制台**。已有配置不会被覆盖。

网页提供任务管理、立即同步、歌单预览、平台歌曲检索、音频文件管理、运行记录和默认规则设置。任务 ID 创建后保持不变。删除任务会保留音频、下载记录和历史。

网页保存会校验并写回当前 YAML，随后重新应用调度，无需重启。正在运行的任务需完成后再编辑或删除；已经排队的任务执行前会读取最新启用状态。网页没有 Key 或 Cookie 输入框，密钥只从进程环境读取。

也可以先复制 [配置示例](config.example.yaml)，在文件中设置任务，再运行 `validate`。手动修改 YAML 后需要重启服务；当前没有文件监视热加载。请避免同时使用外部编辑器和网页修改配置，重启前页面中的文件值可能与内存中实际运行的配置不同。

```powershell
Copy-Item config.example.yaml config.yaml # 仅在尚无 config.yaml 时执行。
.\bin\meloport.exe validate -config config.yaml
```

HTTP 只允许绑定回环地址，默认 `127.0.0.1:8080`。`server.listen: ""` 可关闭网页和健康检查。当前是本机控制台，没有远程登录、多用户或公网管理功能。`Ctrl+C` 正常停止服务。

## 获取与回退规则

| 功能 | 首选来源 | 直连失败后 |
| --- | --- | --- |
| 网易云歌单 | 网易云平台接口，完整成员 ID 与歌曲详情校验 | 回退开关开启时可调用 ChKSz 网易云歌单接口 |
| QQ／酷狗歌单 | 对应平台接口；酷狗部分专题使用平台网页内嵌列表 | 明确失败；ChKSz 文档没有这两个平台的歌单接口 |
| 三平台歌曲搜索 | 对应平台搜索接口 | 回退开关开启时可调用 ChKSz 对应搜索接口 |
| 缺失歌曲音频 | ChKSz 解析 | 记录实际错误，不自动改为其他平台或较低音质 |

成功的直连请求不会再调用 ChKSz；有效空搜索结果也不会触发回退。取消操作不会触发额外回退。回退会在日志中标明平台、操作和原因；歌单预览／同步摘要也区分 `native` 与 `chksz_fallback`。

```yaml
metadata:
  timeout: 30s
  min_interval: 1s
  fallback_to_api: true # 设为 false 可关闭可选的元数据回退。
```

关闭元数据回退不影响下载时必需的音频解析，也不关闭酷狗解析前的 ID 确认。仅本地有效文件已全部存在、且歌单直连成功时，整轮同步不需要 Key，也不会消耗 ChKSz 请求。

## Key 与可选平台 Cookie

从 https://api.chksz.com/login 获取个人 `chksz_` Key。启动、直连搜索和歌单预览不提前校验 Key；真正需要解析音频或回退时才读取。需下载时，在启动服务的终端设置后重新启动：

```powershell
$secureKey = Read-Host 'ChKSz API Key' -AsSecureString
$env:CHKSZ_API_KEY = [System.Net.NetworkCredential]::new('', $secureKey).Password
Remove-Variable secureKey
.\bin\meloport.exe serve -config config.yaml
```

默认 Cookie 环境变量为 `MELOPORT_NETEASE_COOKIE`、`MELOPORT_QQ_COOKIE`、`MELOPORT_KUGOU_COOKIE`，可通过 `metadata.*_cookie_env` 更换名称。公开歌单先尝试匿名请求；登录限制取决于平台。Cookie 仅发给对应平台，不传给 ChKSz 或音频站点；ChKSz Key 同样不会传给平台接口。不要将实际密钥写入 YAML、仓库或订阅链接。

进程环境不会因另一个终端设置变量而改变。更换 Key、Cookie 或它们的环境变量名后，请重新启动服务。

## 任务与时间

所有相对路径以**配置文件目录**为基准，任务 `directory` 相对于 `storage.music_dir`。任务省略 `schedule`、`quality` 时继承全局默认；省略 `directory` 时使用任务 ID。完整选项见 [config.example.yaml](config.example.yaml)。

网页的「系统设置」可直接填写下载根目录。这里填写的是 **Meloport 运行环境中的路径**，不是浏览器所在设备的下载目录；将来部署在 Docker 中时，应填写容器内已挂载的路径。相对路径仍以配置文件目录为基准。保存后，相对路径的任务子目录随新根目录生效，显式配置的绝对任务目录保持不变。更改目录不会移动或删除已有音乐；下次同步会在新目录补齐歌曲，可能重新下载。

同步时间可直接选择每天、工作日、每周、每隔数小时或数分钟，无需填写表达式；任务也可选择继承默认时间。页面以自然语言显示同步计划。已有无法用这些选项表示的自定义计划会保持原样，只有主动选择新规则后才替换。

```yaml
timezone: Asia/Shanghai
schedule: "0 3 * * *"
quality: lossless
run_on_start: false
tasks:
  - id: favorites
    platform: netease
    playlist: "3778678" # 示例；请换成自己的歌单。
  - id: qq-evening
    platform: qq
    playlist: "https://y.qq.com/n/ryqq/playlist/123456789"
    schedule: "0 20 * * *"
    enabled: false
  - id: kg-weekend
    platform: kugou
    playlist: "id_123456" # 专题歌单 ID；纯数字按分享码解释。
    schedule: "30 4 * * 6"
    enabled: false
```

YAML 内部继续使用五字段 cron“分钟 小时 日 月 星期”，不含秒，不接受 `@daily`，网页控件会自动转换。默认中国时区每天 03:00；每个任务可独立覆盖。按整点或整分钟间隔运行的规则以该时区的钟表时间为准。单 worker 串行执行，同一任务运行或排队时合并重复触发。停服期间错过的定时不会自动补跑；可手动运行或设置 `run_on_start: true`。

链接／ID 支持范围：网易云普通完整歌单链接及 ID；QQ 官方完整歌单链接及 ID；酷狗数字分享码、`id_专题ID`、编码 gcid 和带 chain／global_collection_id 的受支持官方链接。不透明短链接需要先展开；未知链接会明确报错。

音质使用各平台原生值，任务中显式填写的值不做别名猜测或自动降级：

| 平台 | 任务可用值 |
| --- | --- |
| 网易云 | `standard`、`exhigh`、`lossless`、`hires`、`jyeffect`、`sky`、`jymaster` |
| QQ／酷狗 | `128k`、`320k`、`flac`、`hires`、`master` |

全局通用偏好在继承时做语义映射，例如 `lossless` → 网易云 `lossless`、QQ／酷狗 `flac`；`standard`／`exhigh`／`jymaster` 分别对应另外两平台的 `128k`／`320k`／`master`。更改音质不会替换已经完整下载的文件，实际可用音质仍由上游决定。

## 命令行

以下预览和检索默认直连平台；回退开启且直连失败时可能消耗 ChKSz 额度。

```powershell
.\bin\meloport.exe playlist -config config.yaml -task favorites
.\bin\meloport.exe search -config config.yaml -platform qq -keyword '歌名或歌手' -limit 20 -offset 0
.\bin\meloport.exe sync -config config.yaml -task favorites
.\bin\meloport.exe retag -config config.yaml -task favorites
.\bin\meloport.exe status -config config.yaml -limit 20
```

不要同时在同一数据库上运行 `serve`、`sync` 或 `retag`，写进程锁会拒绝第二个实例。服务运行时请在网页触发同步任务；只读 `status` 可并行使用。`/healthz` 和 `/readyz` 仍可用于本地健康检查。

## 音频标签与 Navidrome

新下载的音频会自动补齐文件内缺失的歌名、歌手（支持多歌手）和专辑；平台确实返回专辑艺人、曲号、碟号、年份时也会补齐。MP3、FLAC、M4A、Ogg/Opus、WAV、AIFF、APE、WavPack 使用 TagLib 的容器标签，不转码音频；原始 AAC 等不支持写标签的格式保留音频并告警。应用无需额外安装 FFmpeg 或 C 编译器。

封面优先取现有原生歌曲元数据；现有音频解析结果有公开封面地址时也可使用，不新增 ChKSz 元数据请求。仅接受受支持平台的公开 CDN 地址，下载有时限、大小和图片格式限制，封面嵌入音频文件。已有非空标签、封面和其他自定义标签保持不变；信息缺失时不会猜测，不会把歌手头像当作专辑封面。写入在同文件系统的临时副本中完成，回读检查后发布，再登记成品大小和 SHA-256。

下载歌曲时，MeloPort 会按歌曲 ID 从网易云、QQ 音乐或酷狗获取原文歌词，保留行时间戳并写入音频的 `LYRICS` 标签。已有歌词会保留。歌词请求使用原生接口，无需 ChKSz Key，也不消耗其额度。播放器是否能识别内嵌歌词并按时间滚动，取决于播放器对该格式的支持。

升级后，歌单里的旧歌曲会在下次同步时补齐缺失的标签和歌词，直接使用已有音频。成功后记住元数据指纹，后续未改变的同步直接跳过，不会每次复制或哈希整首歌曲。

网页的「音频文件」列出同步记录中的歌曲，可按任务、平台、关键词和缺失项筛选。列表读取文件中的歌名、歌手、专辑、封面和歌词标签。点击某首的「重试补全」后，MeloPort 会从本地记录和原平台获取所需资料，保留已有标签。操作使用本地音频，无需 ChKSz Key。文件缺失、大小已变或不在任务目录时，列表会说明状态。已删除任务的下载记录仍可查看。

也可停止正在运行的服务，手动为数据库中登记的已有音频补标签（包括仍配置在本地任务中、但已从远端歌单移除的歌曲）：

```powershell
# 所有已配置任务（也包括停用任务）
.\scripts\dev.ps1 -Action retag
# 仅一个任务
.\scripts\dev.ps1 -Action retag -Task favorites
```

`retag` 根据本地下载记录补标签，需要时请求原生歌词和封面。此命令无需 ChKSz Key、歌单数据或音频下载。旧版数据库缺少封面、曲号时，可以先执行普通同步更新元数据。文件缺失、大小异常或已不在任务当前目录时会报错保留原状。显式 `retag` 会重新检查文件，耗时取决于曲库大小、磁盘和网络。

平台没有提供歌词时，音频照常保存。歌词请求失败会记录在 `tag_warnings` 和服务日志中，网络恢复后可运行 `retag` 重试。

封面失败不会丢弃可播放音频或成功写入的文字标签：命令行 JSON 的 `tag_warnings` 和服务日志会给出提示。为避免每次定时都重试失败图片，普通同步不反复下载封面；恢复后可显式运行 `retag` 重试。文字标签写入失败也保留音频并提示，后续可重试本地补标签。完成后请在 Navidrome 中重新扫描音乐库。

标签引擎使用内嵌 Wasm 的 go-taglib/TagLib；来源、许可证及替换构建方法见 [第三方说明](docs/THIRD_PARTY.md)。

## 115 网盘、CD2 与 STRM

在 `config.yaml` 中启用 `cloud115`，填写专用的 115 目标目录 `target_cid`。把 `staging_dir` 设为 CD2 单向上传的监控目录，并把该目录的**内容**上传到目标 `cid`，保留任务子目录（远端布局为 `<target_cid>/<任务ID>/<音频文件>`）。在 CD2 中配置确认上传成功后再清理源文件。`storage.music_dir` 和各任务的工作目录需要与 `staging_dir` 位于同一个文件系统，`strm_dir` 使用单独的目录。完整音频会原子移入暂存目录，然后交给 CD2。

使用 [OpenList 官方授权页](https://api.oplist.org/115cloud/requests?driver_txt=115cloud_go&server_use=true) 的 115 网盘验证登录取得 Meloport 专用的短期 Access Token，把它放进启动进程的 `MELOPORT_115_ACCESS_TOKEN` 环境变量（可改 `access_token_env`）。令牌失效时重新授权并重启进程。`cloud-check` 验证令牌和目录访问。加 `-downurl` 可检查一个已完成文件的直链解析，终端只显示结果。

```powershell
.\bin\meloport.exe cloud-check -config config.yaml
.\bin\meloport.exe cloud-check -config config.yaml -downurl
.\bin\meloport.exe serve -config config.yaml
```

Meloport 按准确的文件名、大小和 SHA-1 核对 115 上的成品，再持久化映射和生成 `strm_dir/<任务ID>/<音频文件>.strm`。交给 CD2 后，即使暂存文件已清理，再跑原任务也会跳过该歌曲。若长时间未生成 STRM，先在 CD2 检查失败队列、保留的子目录和远端文件。如果暂存文件被提前移除，需从备份或 CD2 恢复文件，待 115 核对通过后才会导出。停止 `serve` 后可执行 `meloport.exe cloud-reconcile -config config.yaml`，立即核对待上传记录并修复缺失的 STRM。网页「音频文件」检查的是本地工作文件，仍可能把已交给 CD2 的记录显示为本地缺失。

播放器读取 STRM 中的一行媒体地址，Meloport 使用该请求的 User-Agent 取得 115 的临时 HTTPS 直链并返回 302。媒体监听默认使用 `127.0.0.1:8081`。跨设备播放时，把 `media_base_url` 设为播放器可访问的 HTTPS 地址，并把它反代到 `media_listen`。若配置了 URL 路径前缀，反代需去掉该前缀。STRM 链接持有者可以播放对应音频，请保护导出目录并在反代日志中隐藏媒体路径。

使用 Subsonic 客户端时，音乐库扫描目录应指向 CD2 挂载的 115 目标目录。设置 `navidrome_url` 和音乐服务端看到的绝对 `navidrome_mount_root`。在用于直连播放的播放器设置中开启 `Report Real Path`，关闭服务端转码和码率限制，再把客户端连接到 Meloport 媒体端口。普通播放会在上游鉴权及真实路径匹配后尝试 302。请求显式指定转码、码率限制或未找到文件映射时继续由上游处理。使用上游服务的原生鉴权，且不要将 Meloport 加到上游的外部鉴权代理信任列表。配置示例见 [config.example.yaml](config.example.yaml)。首轮账号联调清单见 [115 网盘与 STRM 接入设计](tmp/115网盘与STRM接入设计.md)。

## 数据、去重与恢复

默认 SQLite 为 `data/meloport.db`，音频目录为 `data/music/任务ID/`。文件按 `平台-稳定歌曲ID.实际格式` 命名：网易云用歌曲 ID、QQ 用 MID、酷狗优先使用原生音频 hash。标题／歌手／专辑同时用于文件标签与数据库记录，不因同名歌曲相互覆盖。数据库首次写入会自动升级到支持标签状态的版本。

已有记录还会检查实际文件是否为常规文件、大小是否一致。缺失文件会补下载；大小异常文件仅在解析成功后保留为 `.corrupt-*` 再修复。远端删歌、删任务或改目录均不会自动删除旧音频。

下载采用同目录临时文件、文件头识别、长度校验和 SHA-256 登记，使用硬链接原子发布，因此目标文件系统须支持硬链接；不支持时明确失败。下载大小上限针对原始传输数据，标签和封面允许额外占用至多 16 MiB。不会每轮重新哈希或解码标签已补齐且元数据未变的已有音频，同大小的外部修改不在当前检测范围。强制断电可能留下 `.part` 或 `.meloport-tags-*` 临时目录，停服后可自行确认清理。

崩溃后已有成品但尚无数据库记录时可以恢复登记，当前仍先解析，可能消耗额度并计入 `downloaded`。已登记文件补标签前会写入恢复标记，防止标签写完而数据库尚未更新时被误判为损坏音频。网页登记数量／大小来自数据库，不能代替实时磁盘审计。不同任务分别存储同一歌曲，当前不做跨任务音频共享。歌词和封面写入音频文件本身。

## 酷狗标识与额度

原生酷狗 hash／audio_id 不直接假定为 ChKSz 的 `id`。仅在发现缺失文件、进入音频解析阶段时，通过 ChKSz 酷狗搜索取得其自身 ID，并进行确认：优先匹配相同 hash；否则必须唯一匹配规范化标题、歌手、专辑及相差不超过 2 秒的时长。明确不同 hash、信息不足或多个匹配版本都会报错，不自动选择同名翻唱／现场版。

因此通常每首缺失的网易云／QQ 歌曲需要一次 ChKSz 解析；酷狗还需要一次 ID 确认搜索。原生歌单／搜索请求不消耗 ChKSz 额度，只有实际回退时才计入。依据 [接口文档](docs/ChKSz%20API.md)，默认每分钟 20 次、每日免费 50 次；每个 ChKSz 客户端会强制限制在任意滚动 60 秒内最多 20 次请求，默认请求间隔另设为至少 3.1 秒，平台元数据独立串行间隔默认 1 秒。多个程序共享同一 Key 时，各进程不会合并计数，需自行协调额度。

401／403 会停止当前批次并暂停调度至修复后重启；402 暂停到下一次 UTC+8 零点后等待后续定时；429 仅在有效 `Retry-After` 下最多重试一次，之后至少暂停一分钟；503 也停止当前批次并短暂暂停。普通单首失败保留记录并继续其他歌曲。不自动充值，不无限重试，不静默跨平台替换。

## 验证与实现边界

```powershell
.\scripts\dev.ps1 -Action test
```

普通全量测试、`go vet`、构建与 `CGO_ENABLED=1 go test -race -count=1 ./...` 已通过，覆盖原生来源、ChKSz、配置编辑、同步和网页后端。测试使用本地模拟服务和临时 SQLite，自行关闭资源。普通开发脚本使用 `CGO_ENABLED=0`；竞态检查在可用的本机 C 编译环境执行，没有修改系统工具链。

音频标签使用本地生成的短正弦波样本验证了 MP3、FLAC、M4A、Opus、Ogg、WAV、AIFF、WavPack 的中文、多歌手、内嵌封面、旧标签保留和重复写入。另用独立 FFmpeg/ffprobe 校验上述八种格式标签前后的解码音频 SHA-256 一致，并能读取标签和封面；WavPack 封面显示为 APE 附件。此验证不依赖真实歌曲、ChKSz Key 或实际 Navidrome 实例。

2026-11-20 在未配置 ChKSz Key、关闭元数据回退的情况下，三平台真实歌曲搜索均成功；网易云公开歌单 `3778678` 返回 200/200，QQ 公开歌单 `7217720898` 返回 35/35，酷狗 `id_1067062`「放松轻音：你的舒缓良方」读取到 20 首，另由官方移动页面的 `songcount=20`、`total=20` 佐证。均未调用 ChKSz 或下载音频。

尚未使用真实 ChKSz Key 联调，也未进行真实歌曲下载验收。平台接口、访问限制和返回字段可能变化；专题 HTML 在未声明总数时也无法证明服务端没有隐藏截断。模拟测试只能验证当前协议及错误处理，不能保证每个真实歌单都可匿名读取。

实际 Go 服务的浏览器验收通过 67 项检查，覆盖 1440px 桌面、375px 手机、812px 横屏和键盘对话框操作。9 次真实任务／设置写入、真实进程重启后的配置保留和删任务保留本地文件均已验证；搜索／预览的页面交互使用明确的本地模拟响应，与上述真实平台元数据实测分开。截图及检查结果在被忽略的 `.cache/webui-qa/`。测试服务、隔离浏览器和临时代理均已关闭，测试端口已验证释放。

Windows 本机构建与 Linux amd64 交叉编译已通过。真实音频验收仍需个人 Key；当前没有 Docker 构建、真实 Linux 部署或代码推送。

项目总体设计见 [初始化设计](tmp/初始化设计.md)。临时参考项目调研和网页验收记录保留在本地 `tmp/`，不纳入 Git；参考克隆、缓存、编译产物、配置及实际数据同样由 `.gitignore` 排除。
