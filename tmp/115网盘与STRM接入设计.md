# Meloport 接入 115 OpenAPI 与 STRM

2026-11-21 · 接入设计，待实现与账号联调。

## 目标与首版范围

复用 OpenList 使用的 115 开放平台 AppID，免去自行申请应用。运行时不部署 OpenList 或 CD2；Meloport 自己使用用户授权的令牌直连 115 OpenAPI，完成榜单同步、本地音频上传、远程文件映射和 `.strm` 生成。播放器打开 STRM 时，由 Meloport 通过 115 OpenAPI 按需获取文件下载地址并返回 HTTP 302。

首版以现有平台订阅任务为生产者：本地音频下载和标签处理成功后，进入受限的 115 上传队列；上传完成并确认远程文件可读后，生成或更新本地 STRM。定时同步可以复用现有调度能力。网盘只保留 Meloport 自己写入的远程目录，不自动删除远端文件。

## 授权方式

OpenList 官方公开授权入口当前使用的 115 `client_id` 为 **`100197303`**，于 2026-11-21 从返回的公开登录地址核对。应用 ID 可作为配置输入。用户完成授权后生成的 Access Token 和 Refresh Token 才是访问其网盘的凭据。OpenList 实例里的存储 ID 用于标识挂载记录，115 的 `cid` 用于标识目录。 [官方公开授权入口](https://api.oplist.org/115cloud/requests?driver_txt=115cloud_go&server_use=true)

| 阶段 | 操作 | 验证状态 |
| --- | --- | --- |
| 首次联调 | 仅使用 `api.oplist.org` 的授权页面选择 115 网盘验证登录，勾选「使用 OpenList 提供的参数」，完成授权后将专门为 Meloport 获取的令牌交给本地进程。运行时不依赖 OpenList 实例。 | 官方流程与源码已核对，尚未使用账号实测。 |
| 内建扫码 | 配置 AppID，由 Meloport 后端生成随机 `code_verifier`，调用 115 申请二维码，等待用户确认，再发起换令牌请求。SDK 已实现这套 PKCE 流程，客户端无需 AppSecret。 | SDK 实现已核对。`100197303` 能否在独立客户端完成 PKCE 并取得所需权限，待实测。 |

官方授权页使用 OAuth 授权码模式，AppID 和 AppSecret 由 OpenList 授权服务器提供，首次换取令牌经过该服务。将令牌交给 Meloport 后，列目录、取得下载地址和刷新令牌均可请求 115 的域名。若内建扫码验证失败，首版可以继续使用官方页授权与本地导入。 [OpenList 115 Open 文档](https://doc.oplist.org/guide/drivers/115_open) · [官方授权服务实现](https://github.com/OpenListTeam/OpenList-APIPages/blob/4322fd1f0f5e4e77454fd8a5edb89727854b0092/src/driver/115cloud_oa.ts#L16) · [PKCE SDK 实现](https://github.com/OpenListTeam/115-sdk-go/blob/v0.2.6/auth.go)

### OpenAPI 调用清单

| 操作 | 已核对的请求 | 关键输入 |
| --- | --- | --- |
| 网页授权 | `GET https://passportapi.115.com/open/authorize` | `client_id`、`redirect_uri`、`state` |
| 网页授权码换令牌 | `POST https://passportapi.115.com/open/authCodeToToken` | 授权码与服务端应用凭据 |
| PKCE 申请二维码 | `POST https://passportapi.115.com/open/authDeviceCode` | `client_id`、`code_challenge`、`code_challenge_method=sha256` |
| PKCE 查询扫码状态 | `GET https://qrcodeapi.115.com/get/status/` | 二维码会话的 `uid`、`time`、`sign` |
| PKCE 换令牌 | `POST https://passportapi.115.com/open/deviceCodeToToken` | `uid`、`code_verifier` |
| 刷新令牌 | `POST https://passportapi.115.com/open/refreshToken` | `refresh_token` |
| 验证授权 | `GET https://proapi.115.com/open/user/info` | Access Token |
| 分页列目录 | `GET https://proapi.115.com/open/ufile/files` | Access Token、`cid`、`offset`、`limit` |
| 获取上传凭据 | `GET https://proapi.115.com/open/upload/get_token` | Access Token |
| 初始化上传 | `POST https://proapi.115.com/open/upload/init` | 文件名、大小、SHA-1、目标 `cid` |
| 断点或分片上传 | `POST https://proapi.115.com/open/upload/resume` 及返回的对象存储地址 | 上传会话和分片信息 |
| 获取文件下载地址 | `POST https://proapi.115.com/open/ufile/downurl` | Access Token、`pick_code`、User-Agent |

网盘业务请求使用 `Authorization: Bearer <Access Token>`。扫码状态是授权过程的辅助接口，列文件与取地址由开放平台的 `/open/` 接口处理。 [SDK 接口定义](https://github.com/OpenListTeam/115-sdk-go/blob/v0.2.6/const.go) · [文件请求实现](https://github.com/OpenListTeam/115-sdk-go/blob/v0.2.6/fs.go)

### 凭据生命周期

- 首次连通验证可以从环境变量注入短期 Access Token，只调用用户信息、列文件和取地址。配置文件记录 AppID、目录和凭据引用等设置项。
- 常驻运行需要持久化轮换后的令牌。一个授权记录只由 Meloport 一个进程负责刷新，同一进程内将并发刷新合并，成功后立即持久化新令牌。不要让其他客户端或服务共享并刷新这份令牌。令牌失效时提示重新授权。
- 实现常驻续期前，先确定凭据后端以及它与项目环境变量规则的衔接。可评估系统凭据存储或外部密钥存储。普通 YAML、业务 SQLite、STRM、日志及 API 响应只保留配置、状态与脱敏信息。

OpenList 驱动源码明确说明刷新令牌轮换后旧值会失效。 [OpenList 刷新处理](https://github.com/OpenListTeam/OpenList/blob/7c26f1012c2951441233dce4ef309753fc76165d/drivers/115_open/driver.go#L43)

## 扫描与播放

```mermaid
flowchart LR
    A[用户完成 115 授权] --> B[Meloport 持有令牌]
    B --> C[平台榜单同步与本地下载]
    C --> D[通过 OpenAPI 上传并确认]
    D --> E[保存远程映射并生成 STRM]
    E --> F[播放器请求 Meloport 媒体地址]
    F --> G[通过 OpenAPI 取得当前下载地址]
    G --> H[HTTP 302 到 115 CDN]
```

1. **同步和上传。** 平台榜单同步仍由现有任务完成。下载、标签和本地完整性检查成功后，上传器以文件 SHA-1/大小参与 115 的初始化和秒传判断，使用单一队列、低并发和可恢复的分片上传。只有远程文件确认可列出或可取 `downurl` 后才把状态标记为可播放。
2. **映射。** 本地数据库保存 `task_id`、平台歌曲 ID、本地路径、115 `cid`、文件 ID、`pick_code`、远程路径、文件哈希、上传状态和稳定播放 ID。临时 CDN URL 不持久化。
3. **导出。** 在独立目录按任务或专辑生成 STRM。文件内容是一行 UTF-8 的稳定 Meloport 媒体地址，例如 `https://media.example.net/media/ab12cd34`。扫描或重试只更新缺失或内容变化的 STRM，不因远端歌单变化删除已上传文件。
4. **播放。** 媒体接口根据稳定播放 ID查询映射，用当前 `pick_code` 和请求中的 User-Agent 调用 `downurl`，返回 `HTTP 302 Location`。缓存只保存短期直链，且不能超过 115 直链有效期。若 302 后的 CDN 对 Range 或 User-Agent 有限制，再增加受控代理回退；首版先验证 302、拖动和断点续播。
5. **访问。** `server.listen` 继续作为回环管理入口，新增独立的媒体监听配置，或由外部 HTTPS 反向代理暴露媒体入口。STRM 中填写播放器实际可到达的媒体 URL，媒体入口必须有访问控制或不可猜测的稳定 ID。

Navidrome 目前不支持把音频 STRM 当作曲库文件。STRM 需要交给支持该格式的播放器或媒体程序；Meloport 的媒体入口本身不依赖 Navidrome。 [Navidrome 官方讨论](https://github.com/navidrome/navidrome/discussions/5296)

## MusicWarp 借鉴边界

MusicWarp 只作为代码模式参考，不作为独立运行时服务。把需要的媒体 handler 和直链缓存能力实现到 Meloport 自己的 Go 服务中。参考 `E:\project\musicwarp\musicwarp\internal\handler\stream.go` 的 302 处理与临时缓存，及 `E:\project\musicwarp\musicwarp\internal\service\openlist.go` 中“先解析下载地址，再返回给 handler”的职责拆分；其 OpenList 客户端需替换为 Meloport 的 115 OpenAPI 客户端。不要引入它对 Navidrome Song ID、Navidrome 真实路径、OpenList `/api/fs/get` 的依赖；Meloport 已经拥有自己的歌曲元数据和 115 远程映射。

管理接口当前只允许回环监听。媒体接口应由同一 Meloport 进程的独立监听配置提供，或由外部 HTTPS 反向代理安全暴露。随机不透明媒体 ID 是可转发的播放凭据：持有 STRM 链接的人可以播放对应文件，应使用 HTTPS、避免在日志中记录完整 URL，并保护本地 STRM 文件。

## Meloport 实现顺序

| 顺序 | 实现内容 | 当前参考位置 |
| --- | --- | --- |
| 1 | 封装 115 连接与凭据读取，先验证 `UserInfo`、`List` 和 `DownURL` | `tmp/references/115-sdk-go/` 和 `tmp/references/openlist/drivers/115_open/`（仅作 API 参考） |
| 2 | 增加上传队列、分片恢复和远程文件映射，复用同步运行摘要 | `internal/config/config.go`、`internal/app/`、`internal/state/` |
| 3 | 增加独立 STRM 写入和稳定媒体 302 入口，管理端与媒体端分开监听 | `internal/syncer/syncer.go`、`internal/download/`、`internal/state/` |
| 4 | 验证凭据轮换、定时同步、Range/UA 和目标播放器，再决定是否集成 PKCE 扫码 UI | `tmp/references/115-sdk-go/auth.go` 与 `tmp/references/openlist-api-pages/src/driver/115cloud_oa.ts`（仅作授权参考） |

本地参考仓库位于 Git 忽略的 `tmp/references/`。对应版本为 OpenList `7c26f10`、OpenList-APIPages `4322fd1`、115 Go SDK `v0.2.6`。这些目录只供查阅，独立检出时可按上面的上游链接获取对应源码。

## 接入验收

- [ ] **授权与读取。** 使用官方页预设参数取得专用令牌，对一个含有自有测试音频的小目录验证用户信息、分页文件列表和 `downurl`。记录结果、状态码与文件数量，测试输出中省略令牌和临时地址。
- [ ] **上传与导出。** 覆盖中文与空格、同名不同扩展、安全路径、重复扫描、分片中断和重试。确认远程文件确认可读后才生成 `.strm`，重复运行不重复上传。
- [ ] **真实播放。** 从目标播放器的运行环境索引至少一首音频，验证首播、拖动、暂停后续播和服务重启后仍能用同一 STRM 播放。验证 Range 返回符合预期；取得新下载地址后继续使用原有 STRM。
- [ ] **续期。** 本地模拟服务覆盖令牌轮换、多个请求同时触发刷新、持久化失败和重启读取新令牌。真实账号另验证至少一次成功刷新。
- [ ] **独立扫码。** 使用 `100197303` 单独验证设备码申请、扫码、换令牌，以及用新令牌列目录和取地址。根据实际结果决定内建扫码是否可发布。
