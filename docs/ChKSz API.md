# ChKSz API 智能体接入提示词

以下内容是给 AI 智能体使用的中文接入提示词。首页的复制按钮会直接复制本文件的完整内容。

---

## 1. 给 AI 智能体的指令

你可以代表用户调用 ChKSz API 完成多平台音乐搜索、歌曲解析、歌词读取和歌单读取。

调用任何业务接口前，用户必须拥有个人 API Key。如果用户没有 Key，请指导其完成以下步骤：

1. 访问 `https://api.chksz.com/login`。
2. 使用邮箱或 LinuxDo 登录。
3. 在账户页查看并复制以 `chksz_` 开头的个人 API Key。

所有业务接口都必须通过 URL 查询参数传入 `apikey`。不要编造、猜测、记录或向第三方泄露用户的 Key。收到 `401`、`402`、`403` 或 `429` 时，应把 API 返回的错误信息准确转述给用户，不要自动无限重试。

## 2. 服务信息

- 基础地址：`https://api.chksz.com`
- 响应格式：除音频转发、纯文本和显式跳转外均为 JSON；具体字段以各接口说明为准
- 默认速率限制：每个 Key、用户和 IP 每分钟 20 次
- 默认免费额度：每个账户每日 50 次
- 每日结算时区：中国标准时间 UTC+8
- 付费额度：1 LDC 可兑换 10 次请求
- API 密钥格式：`chksz_` 加 URL 安全字符组成的字符串

每个业务响应会包含以下额度响应头：

```text
X-RateLimit-Limit: 20
X-Quota-Free-Remaining: 37
X-Quota-Paid-Remaining: 120
```

限流响应还会包含：

```text
Retry-After: 42
```

## 3. 网易云音乐解析

```http
GET /api/163_music?id={歌曲ID}&level={音质}&type={返回类型}&apikey={Key}
```

参数：

| 参数 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `id` | 是 | 无 | 网易云音乐歌曲 ID |
| `level` | 否 | `jymaster` | `standard`、`exhigh`、`lossless`、`hires`、`jyeffect`、`sky`、`jymaster` |
| `type` | 否 | `json` | `json` 返回完整信息，`text` 返回纯 URL，`down` 返回 302 跳转 |
| `apikey` | 是 | 无 | 用户个人 API Key |

示例：

```http
GET https://api.chksz.com/api/163_music?id=2034742057&level=lossless&apikey=chksz_xxx
```

## 4. 网易云音乐搜索

```http
GET /api/163_search?keyword={关键词}&limit={数量}&offset={偏移量}&apikey={Key}
```

| 参数 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `keyword` | 是 | 无 | 歌名、歌手或专辑关键词 |
| `limit` | 否 | `30` | 返回数量 |
| `offset` | 否 | `0` | 分页偏移量 |
| `apikey` | 是 | 无 | 用户个人 API Key |

## 5. 网易云歌词

```http
GET /api/163_lyric?id={歌曲ID}&apikey={Key}
```

`id` 是歌曲 ID。`data` 中以下字段均为字符串，缺失时返回空字符串：

| 字段 | 含义 |
| --- | --- |
| `lrc` | 普通按行歌词，保留原有格式 |
| `tlyric` | 普通歌词的翻译 |
| `romalrc` | 普通歌词的罗马音 |
| `klyric` | 保留原兼容字段；TuneHub 的逐字原文仍放在这里，格式由提供商决定 |
| `yrc` | 网易云 YRC 逐字歌词 |
| `ytlrc` | 与 YRC 对应的翻译，若上游提供 |
| `yromalrc` | 与 YRC 对应的罗马音，若上游提供 |

例如自造 YRC `[1000,2000](1000,500,0)你(1500,1500,0)好` 中，行标记是“开始时间、持续时长”，词标记是“开始时间、持续时长、保留值”，单位均为毫秒。词开始时间相对于整首歌，不再叠加行时间；一个片段可能包含多个字符。

优先解析非空且有效的 `yrc`，没有逐字数据时回退 `lrc`；只有实际采用 YRC 时才优先选 `ytlrc`，否则使用 `tlyric`。不能从普通 LRC 还原真实逐字时间，也不能仅凭某个字段非空认定其格式。

本次为原响应增加三个字段，不改变路径、GET/表单 POST、鉴权和额度规则。沿用网易云 `/eapi/song/lyric`，不切换可能改变 LRC 分行的 `/v1`。各源仍按管理员设置的顺序查询；普通歌词成功但没有 YRC 时不额外请求备用源。TuneHub 保留 `klyric`，新增三个字段返回空字符串。

歌曲 `1974443814` 可用于逐字功能联调；上游数据会变化。原示例 `1315196858` 在 2026-11-07 实测中为纯音乐，不能作为逐字功能的正向样本。空歌词不等于请求成功，更不能据此断定纯音乐；上游错误通过现有失败/备用源流程处理。

## 6. 网易云歌单详情

```http
GET /api/163_playlist?id={歌单ID}&apikey={Key}
```

响应包含歌单名称、封面、创建者、曲目数量和歌曲列表。大型歌单响应可能较大，调用方应设置合理超时。

## 7. QQ 音乐点歌

搜索：

```http
GET /api/qq_music?msg={关键词}&apikey={Key}
```

选择搜索结果中的歌曲：

```http
GET /api/qq_music?msg={关键词}&n={序号}&apikey={Key}
```

也可以通过 `mid` 直接解析歌曲。QQ 音乐接口仅接受 GET。

可选参数：`num` 或 `g` 控制搜索数量（1～50）。`size` 使用音乐源原生值：
`128k`、`320k`、`flac`、`hires`、`master`，默认 `flac`；服务端不会做别名或
降级映射。`cookie` 仅为兼容旧调用保留，当前不会使用或转发。

搜索响应顶层包含 `code`、`msg`、`count` 和 `list`；列表项包含
`n`、`name`、`singer`、`album`、`pay`、`mid`。歌曲详情响应顶层包含
`url`，并可包含 `name`、`singer`、`album`、`cover`、`lrc`、`interval`、
`mid`、`bitrate`、`format`。当音乐源未返回封面时，`cover` 会使用 QQ 官方
专辑封面地址补全。`type=text` 时歌曲详情直接返回播放 URL。

## 8. 酷狗音乐点歌

搜索：

```http
GET /api/kugou_music?msg={关键词}&apikey={Key}
```

选择搜索结果中的歌曲：

```http
GET /api/kugou_music?msg={关键词}&n={序号}&apikey={Key}
```

酷狗音乐接口仅接受 GET。`n` 可选择搜索结果序号（1～50），`id` 可直接解析
搜索结果中的歌曲 ID。`size` 使用原生值 `128k`、`320k`、`flac`、`hires`、
`master`，默认 `flac`，不会做别名或降级映射。

搜索响应顶层包含 `code`、`msg`、`keyword`、`total` 和 `list`；列表项固定包含
`n`、`id`、`name`、`singer`、`album`、`duration`。详情响应包含 `url`，并固定
映射 `name`、`singer`、`album`、`cover`、`lrc`、`interval`、`bitrate`、
`format`、`id`。`type=text` 时详情直接返回播放 URL。

`/api/kg_stream` 仍是受限的兼容媒体通道，不应把任意第三方 URL 传给该接口。

## 9. 错误处理

| HTTP 状态 | 含义 | 智能体应采取的动作 |
| --- | --- | --- |
| `400` | 参数缺失或格式错误 | 检查参数，不要原样重复请求 |
| `401` | 缺少 Key、Key 无效或登录失效 | 请用户检查 Key 或重新登录 |
| `402` | 免费和付费额度均已用尽 | 告知用户等待次日免费额度或兑换 LDC |
| `403` | 用户、Key 或 IP 被封禁 | 转述原因，停止自动重试 |
| `404` | 路径或资源不存在 | 检查接口路径和资源 ID |
| `429` | 超过速率限制 | 按 `Retry-After` 等待后最多重试一次 |
| `503` | API 已被管理员停用或音乐服务暂时不可用 | 稍后重试，不要高频轮询；管理员停用不会消耗额度 |

## 10. 调用规则

1. 优先使用 HTTPS。
2. 不要把 API Key 写入日志、公开链接、截图或代码仓库。
3. 不要高并发调用音乐服务；批量任务应限制并发并遵守 `Retry-After`。
4. `163_music`、`163_search`、`163_lyric`、`163_playlist` 支持 GET，也支持表单 POST；QQ 和酷狗接口仅支持 GET。
5. 未知 `/api/*` 路径返回 404，不会扣减额度。
6. 业务失败时以 HTTP 状态和 `msg` 为准，不要只检查 JSON 中是否存在 `data`。
