# 音频标签组件

## go-taglib 与 TagLib

- 模块：`go.senan.xyz/taglib v0.15.0`
- 上游：<https://github.com/sentriz/go-taglib/tree/v0.15.0>
- 许可证：LGPL-2.1；原文随本项目保留于 [licenses/go-taglib-LGPL-2.1.txt](licenses/go-taglib-LGPL-2.1.txt)。
- 标签引擎：该模块内嵌的 TagLib 2.1.1 WebAssembly 模块，使用 wazero 在 Go 进程内运行，无 CGo 或外部 FFmpeg 依赖。wazero 使用 Apache-2.0，原文随本项目保留于 [licenses/wazero-Apache-2.0.txt](licenses/wazero-Apache-2.0.txt)。

本项目未修改该依赖的 Go 源码或 Wasm。可从上述固定版本取得源码、TagLib 子模块及其构建脚本；其 README 的 “Manually Building and Using the Wasm Binary” 说明了重建过程。项目本地构建步骤：

1. 克隆上游固定标签及子模块：

   ```text
   git clone --recursive --branch v0.15.0 https://github.com/sentriz/go-taglib.git
   ```

2. 按上游说明，用 WASI SDK 与 Binaryen 执行 `build/build.sh`；或执行该依赖自己的 `build/build-docker.sh`，生成替代的 `taglib.wasm`。这只是依赖的重建选项，不改变 Meloport 暂不提供 Docker 打包的开发约定。

3. 在 Meloport 源码目录中，使用 Go 1.26 或更新版本编译，并把 `binaryPath` 指向本地替代 Wasm 的绝对路径：

   ```powershell
   $env:CGO_ENABLED = '0'
   go build -ldflags="-X go.senan.xyz/taglib.binaryPath=E:/path/to/taglib.wasm" -o bin/meloport.exe ./cmd/meloport
   ```

也可以在模块依赖中以 `go mod edit -replace` 指向自行修改、重建的本地 go-taglib，再从源码重新编译。固定依赖的校验信息保留于 `go.sum`。
