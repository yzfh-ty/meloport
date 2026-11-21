# Meloport 接入 115 OpenAPI 与 STRM

2026-11-21 · 接入设计，待实现与账号联调。

## 目标与首版范围

复用 OpenList 的 115 开放平台 AppID，免去自行申请应用。Meloport 使用用户授权的令牌直连 115 OpenAPI，扫描指定目录中的现有音频，在独立的本地目录生成 `.strm`。播放器打开 STRM 时，由 Meloport 通过 115 OpenAPI 按需获取文件下载地址。

首版将 115 目录导出作为独立任务，对网盘仅执行列目录、查看文件和取得下载地址等读操作。定时扫描可以复用现有调度能力。上传 115 和歌单与网盘文件自动匹配另行设计。

## 授权方式

OpenList 官方公开授权入口当前使用的 115 `client_id` 为 **`100197303`**，于 2026-11-21 从返回的公开登录地址核对。应用 ID 可作为配置输入。用户完成授权后生成的 Access Token 和 Refresh Token 才是访问其网盘的凭据。OpenList 实例里的存储 ID 用于标识挂载记录，115 的 `cid` 用于标识目录。 [官方公开授权入口](https://api.oplist.org/115cloud/requests?driver_txt=115cloud_go&server_use=true)

| 阶段 | 操作 | 验证状态 |
| --- | --- | --- |
| 首次联调 | 在 `api.oplist.org` 选择 115 网盘验证登录，勾选「使用 OpenList 提供的参数」，完成授权后将专门为 Meloport 获取的令牌交给本地进程。用户无需运行 OpenList 实例。 | 官方流程与源码已核对，尚未使用账号实测。 |
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
| 获取文件下载地址 | `POST https://proapi.115.com/open/ufile/downurl` | Access Token、`pick_code`、User-Agent |

网盘业务请求使用 `Authorization: Bearer <Access Token>`。扫码状态是授权过程的辅助接口，列文件与取地址由开放平台的 `/open/` 接口处理。 [SDK 接口定义](https://github.com/OpenListTeam/115-sdk-go/blob/v0.2.6/const.go) · [文件请求实现](https://github.com/OpenListTeam/115-sdk-go/blob/v0.2.6/fs.go)

### 凭据生命周期

- 首次连通验证可以从环境变量注入短期 Access Token，只调用用户信息、列文件和取地址。配置文件记录 AppID、目录和凭据引用等设置项。
- 常驻运行需要持久化轮换后的令牌。一个授权记录只由一个进程负责刷新，同一进程内将并发刷新合并，成功后立即持久化新令牌。已有 OpenList 挂载和 Meloport 各持有自己的授权记录。令牌失效时提示重新授权。
- 实现常驻续期前，先确定凭据后端以及它与项目环境变量规则的衔接。可评估系统凭据存储或外部密钥存储。普通 YAML、业务 SQLite、STRM、日志及 API 响应只保留配置、状态与脱敏信息。

OpenList 驱动源码明确说明刷新令牌轮换后旧值会失效。 [OpenList 刷新处理](https://github.com/OpenListTeam/OpenList/blob/7c26f1012c2951441233dce4ef309753fc76165d/drivers/115_open/driver.go#L43)

## 扫描与播放

```mermaid
flowchart LR
    A[用户完成 115 授权] --> B[Meloport 持有令牌]
    B --> C[通过 OpenAPI 分页列出音频]
    C --> D[生成本地 STRM]
    D --> E[播放器请求 Meloport 媒体地址]
    E --> F[通过 OpenAPI 取得当前下载地址]
    F --> G[跳转或代理音频]
```

1. **扫描。** 根据账号连接和根目录 `cid` 分页遍历，过滤 `.flac`、`.mp3`、`.m4a` 等音频。文件身份使用连接标识和 115 文件 ID，播放解析保留 `pick_code`。规范化本地文件名，再用稳定标识消解同名、不同扩展名和大小写碰撞。写入路径必须位于导出目录内。目录或分页读取失败时报告失败，保留已有 STRM。
2. **导出。** 在独立目录按子目录生成 STRM。文件内容是一行 UTF-8 的绝对 HTTP(S) 地址，例如 `https://media.example.net/stream/ab12cd34.flac`。用应用内的稳定媒体标识关联 115 文件，在同一文件系统内通过临时文件发布。重复扫描补齐缺失的 STRM，对已无变化的内容跳过写入，远程删文件时保留本地记录供用户确认。
3. **播放。** 媒体接口根据稳定标识查询映射，用 `pick_code` 请求 `downurl`。先用目标播放器验证 302 跳转能否播放、拖动和断点续播。若 CDN 对 User-Agent 有要求，或客户端无法正确处理跳转，改用媒体代理并保证申请直链与上游读取时的 User-Agent 一致，正确处理 `Range`、`206` 和 `Content-Range`。临时 CDN 地址只参与当前播放。
4. **访问。** `server.listen` 继续作为回环管理入口。异机播放器使用单独的媒体入口，部署在可信内网或具备访问控制的反向代理后，且在 STRM 中填写播放器实际能到达的 URL。私有播放链接按所选鉴权方式管理。

Navidrome 目前不支持把音频 STRM 当作曲库文件。若目标是现有 Navidrome 曲库，另行验证网盘挂载和标签读取。验收 STRM 时，先确认目标播放器能够索引并播放音频 `.strm`。 [Navidrome 官方讨论](https://github.com/navidrome/navidrome/discussions/5296) · [OpenList STRM 驱动](https://doc.oplist.org/guide/drivers/strm)

## Meloport 实现顺序

| 顺序 | 实现内容 | 当前参考位置 |
| --- | --- | --- |
| 1 | 封装 115 连接与凭据读取，先验证 `UserInfo`、`List` 和 `DownURL` | `tmp/references/115-sdk-go/` 和 `tmp/references/openlist/drivers/115_open/` |
| 2 | 增加 115 目录导出任务与媒体映射，复用调度与运行摘要 | `internal/config/config.go`、`internal/app/`、`internal/state/` |
| 3 | 增加独立 STRM 写入、预览、增量更新与播放入口 | `internal/syncer/syncer.go` 可参考任务执行方式。`internal/download/` 和 `downloads` 表继续记录实际下载的音频。 |
| 4 | 验证凭据轮换、定时扫描与目标播放器，再决定是否集成 PKCE 扫码 UI | `tmp/references/115-sdk-go/auth.go` 与 `tmp/references/openlist-api-pages/src/driver/115cloud_oa.ts` |

本地参考仓库位于 Git 忽略的 `tmp/references/`。对应版本为 OpenList `7c26f10`、OpenList-APIPages `4322fd1`、115 Go SDK `v0.2.6`。这些目录只供查阅，独立检出时可按上面的上游链接获取对应源码。

## 接入验收

- [ ] **授权与读取。** 使用官方页预设参数取得专用令牌，对一个含有自有测试音频的小目录验证用户信息、分页文件列表和 `downurl`。记录结果、状态码与文件数量，测试输出中省略令牌和临时地址。
- [ ] **本地导出。** 覆盖中文与空格、同名不同扩展、安全路径、重复扫描和分页失败。确认输出目录只生成 `.strm`，重复运行得到同一个入口。用模拟接口记录请求并确认网盘操作均在只读范围内。
- [ ] **真实播放。** 从目标播放器的运行环境索引至少一首音频，验证首播、拖动、暂停后续播和服务重启后仍能用同一 STRM 播放。验证 Range 返回符合预期；取得新下载地址后继续使用原有 STRM。
- [ ] **续期。** 本地模拟服务覆盖令牌轮换、多个请求同时触发刷新、持久化失败和重启读取新令牌。真实账号另验证至少一次成功刷新。
- [ ] **独立扫码。** 使用 `100197303` 单独验证设备码申请、扫码、换令牌，以及用新令牌列目录和取地址。根据实际结果决定内建扫码是否可发布。
