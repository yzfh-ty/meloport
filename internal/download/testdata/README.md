# 音频标签测试样本

`tone.mp3`、`tone.flac`、`tone.m4a`、`tone.opus`、`tone.ogg`、`tone.wav`、`tone.aiff`、`tone.wv` 均由本项目用 FFmpeg 本地生成：单声道、48 kHz、440 Hz 正弦波，时长约 0.12 秒。不含真实歌曲或从第三方曲库复制的音频。

生成命令，按扩展名分别使用 `libmp3lame`、`flac`、`aac`、`libopus`、`libvorbis`、PCM、`wavpack` 编码：

```text
ffmpeg -f lavfi -i sine=frequency=440:sample_rate=48000:duration=0.12 -ac 1 -map_metadata -1 -fflags +bitexact -c:a <codec> tone.<extension>
```

测试使用固定文件，不要求安装 FFmpeg。它们用于真实容器标签、封面、多歌手和音频属性的读回验证；应用运行也不依赖 FFmpeg。
