<div align="center">

<img src="internal/webui/assets/meloport-icon.png" alt="Meloport Logo" width="100" height="100" />

# Meloport · 乐港

**把喜欢的歌单，留在自己的音乐库。**

订阅网易云、QQ 音乐与酷狗歌单，自动增量同步到本地。<br />
可选接入 115 网盘、STRM 与 Navidrome。

<p>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.26+" /></a>
  <img src="https://img.shields.io/badge/Web_UI-内置控制台-2563EB" alt="内置 Web 控制台" />
  <a href="https://github.com/yzfh-ty/meloport/stargazers"><img src="https://img.shields.io/github/stars/yzfh-ty/meloport?style=flat&amp;logo=github" alt="GitHub Stars" /></a>
  <a href="https://github.com/yzfh-ty/meloport/issues"><img src="https://img.shields.io/github/issues/yzfh-ty/meloport?style=flat" alt="GitHub Issues" /></a>
</p>

<p>
  <a href="#功能特性">功能特性</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="docs/usage.md">使用指南</a> ·
  <a href="docs/115-strm.md">115 / STRM / Navidrome</a> ·
  <a href="https://github.com/yzfh-ty/meloport/issues">问题反馈</a>
</p>

</div>

---

## 项目简介

Meloport 是一个基于 Go 的音乐歌单同步工具，将歌单订阅、定时下载、音频标签和媒体库接入集中在一个 Web 控制台中。歌单、搜索和歌曲信息优先直连音乐平台，缺少的音频通过 ChKSz 解析下载。

默认将歌曲保存在本地，保留音频原有格式。远端歌单移除歌曲或删除订阅时，已下载的音频仍会保留。

## 功能特性

| 功能 | 说明 |
| --- | --- |
| 🎵 多平台订阅 | 支持网易云音乐、QQ 音乐、酷狗的歌单与排行榜，也可直接搜索歌曲 |
| ⏰ 定时增量同步 | 按全局或单个订阅的计划同步，只下载缺少的歌曲 |
| ♻️ 本地音频复用 | 校验已登记歌曲的版本与文件完整性，在不同订阅间复用音频，减少重复解析和下载 |
| 🏷️ 标签与封面 | 补齐歌名、歌手、专辑、封面和歌词，支持筛选缺失信息并重试补全 |
| 🖥️ 内置 Web 控制台 | 单个 Go 程序提供订阅管理、歌曲检索、运行记录与系统设置 |
| ☁️ 115 与 STRM | 可选由 CD2 上传到 115，核对远端文件后导出 STRM |
| 🎧 Navidrome 接入 | 支持接入 Navidrome，并将订阅同步为媒体库中的歌单；本地模式也可使用 |

## 快速开始

以下以 Windows / PowerShell 为例，需要 **Go 1.26 或更新版本**。在项目根目录操作。

### 1. 编译程序

```powershell
.\scripts\dev.ps1 -Action build
```

### 2. 设置管理员密钥

在项目根目录新建 `config.yaml`，填入以下最小配置。已有配置时，只修改对应的 `server.admin_password` 字段。

```yaml
server:
  listen: "127.0.0.1:8080"
  admin_password: "REPLACE_WITH_YOUR_OWN_PASSWORD"

tasks: []
```

将示例密码替换为自己的密钥，长度为 **12–1024 个 UTF-8 字节**。本地 `config.yaml` 已被 Git 忽略。环境变量配置、密钥优先级和 Linux / WSL 说明见[控制台登录指南](docs/login.md)。

### 3. 启动并添加订阅

```powershell
.\bin\meloport.exe serve -config config.yaml
```

打开 **[http://127.0.0.1:8080](http://127.0.0.1:8080/)**，输入管理员密钥登录：

1. 在「系统设置」设置下载目录、默认同步时间和音质。
2. 在「任务概览」添加订阅，选择平台并粘贴歌单链接或 ID；也可从「排行榜」订阅榜单。
3. 点击「添加并开始下载」，在「运行记录」查看进度和结果。

按 `Ctrl+C` 停止服务。管理控制台仅监听本机回环地址。

> **音源解析密钥**：启动控制台、原生搜索与歌单预览无需 ChKSz Key。下载缺少的音频需要个人 [ChKSz API Key](https://api.chksz.com/login)，通过 `CHKSZ_API_KEY` 环境变量或本地 `config.yaml` 的 `api.key` 配置，设置后重启服务。管理员密钥与音源解析密钥各自独立。

## 扩展音乐库

默认使用本地下载模式。115 交接、独立媒体端口、Navidrome 接入和歌单同步均需手动开启。

- **本地音乐库**：订阅歌曲下载到本地，交给自己的音乐播放器或媒体服务管理。
- **115 + CD2 + STRM**：Meloport 下载音频并交给 CD2 上传，核对 115 远端文件后生成 STRM。
- **Navidrome**：接入已有服务，可将普通歌单订阅和排行榜同步为 Navidrome 歌单。

目录配置、开关说明与排错步骤见 [115 / STRM / Navidrome 配置指南](docs/115-strm.md)。真实账号授权、CD2 上传与 Navidrome 客户端实际播放仍需联调，详见[验证范围](docs/usage.md#本地开发)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [使用指南](docs/usage.md) | 添加订阅、配置 ChKSz Key、增量复用、标签补全与命令行操作 |
| [控制台登录](docs/login.md) | 管理员密钥、环境变量、会话与 Linux / WSL 配置 |
| [配置示例](config.example.yaml) | 完整 YAML 字段、平台 Cookie 环境变量与任务规则 |
| [115 / STRM / Navidrome](docs/115-strm.md) | CD2 交接、远端核对、播放入口与媒体库接入 |
| [第三方组件与许可证](docs/THIRD_PARTY.md) | 依赖组件和许可证说明 |

## 本地开发

```powershell
.\scripts\dev.ps1 -Action dev -Config config.yaml
```

也可双击 `start-test.cmd`。修改网页资源后刷新浏览器，修改 Go 代码后重启开发服务。

```powershell
.\scripts\dev.ps1 -Action test
.\scripts\dev.ps1 -Action build
```
