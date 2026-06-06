# SyncWatch — 私人异地同步观影

单 Host 模式的私人异地同步观影系统。

A（Host）本地播放影片，实时解码转码为 WebRTC 流；B（Viewer）打开浏览器输入密码即可同步观看。所有 Viewer 看到完全一致的画面、声音、字幕和播放进度。

## 特性

- 🎬 **一人播放，多人同步** — 画面 / 声音 / 字幕 / 进度完全一致
- 🔗 **WebRTC P2P** — 低延迟传输，≤1080p VP8 + Opus 实时转码
- 📝 **完整字幕支持** — ASS/SSA 特效（描边、阴影、字体样式），SRT/VTT
- 🎵 **多音轨** — 自动检测、一键切换
- ⚡ **倍速播放** — 0.5x / 1x / 1.25x / 1.5x / 2x
- 💬 **在线聊天** — 文本消息 + 系统通知
- 📱 **PWA** — 手机/桌面安装，全屏运行，响应式布局
- 🔒 **安全** — Argon2 密码哈希、JWT 鉴权、登录限流
- 🐳 **单二进制部署** — 13MB 静态编译，支持 Linux / Docker / NAS

## 快速开始

### 直接运行

```bash
# 前置依赖：FFmpeg
ffmpeg -version

# 下载或编译
go build -o syncwatch .

# 创建配置（使用你的密码哈希）
cp config.example.yaml config.yaml

# 启动
./syncwatch --config config.yaml
```

打开对应地址：

| 地址 | 用途 |
|------|------|
| `http://host-ip:8080/` | 观影入口（Viewer） |
| `http://host-ip:8080/admin` | 管理入口（Host） |

### Docker

```bash
mkdir media data
cp config.example.yaml data/config.yaml
# 编辑 data/config.yaml，设置密码哈希

docker compose up -d
```

## 配置

```yaml
server:
  host: "0.0.0.0"
  port: 8080

transcode:
  video_codec: "libvpx"          # VP8（WebRTC 标准编码）
  audio_codec: "libopus"         # Opus（WebRTC 标准编码）
  video_bitrate: "2000k"         # 视频码率
  audio_bitrate: "128k"          # 音频码率
  max_resolution: "1920x1080"    # 最大分辨率（超过自动降采样）
  fps: 30

webrtc:
  stun_servers:
    - "stun:stun.l.google.com:19302"
  #turn_servers:                  # 可选，NAT 穿透
  #  - urls: ["turn:your-turn:3478"]
  #    username: ""
  #    credential: ""

auth:
  password_hash: "$argon2id$..."        # 观影密码
  admin_password_hash: "$argon2id$..."   # 管理密码（不填则共用观影密码）
  rate_limit_per_min: 5
```

### 生成密码哈希

```go
// 使用 Go 生成 Argon2id 哈希
import "golang.org/x/crypto/argon2"
// 或启动时留空 password_hash，控制台会输出默认密码的哈希
```

## 使用说明

### Host（管理者）

1. 打开 `http://host-ip:8080/admin`
2. 输入管理密码，进入控制台
3. 选择本地影片文件或输入视频 URL
4. 控制播放：Space 暂停、←→ 快退快进

管理功能：
- 播放 / 暂停 / 跳转进度
- 切换音轨 / 字幕
- 调整倍速（0.5x–2x）
- 选择影片文件或网络视频 URL
- 查看在线人数和系统状态

### Viewer（观看者）

1. 打开 `http://host-ip:8080/`
2. 输入观影密码
3. 自动同步观看，无需任何操作

### 字幕

将字幕文件放在视频同目录下，与视频同名即可自动加载：

```
movie.mp4
movie.ass     ← 自动加载
movie.chi.srt ← 语言后缀也支持
```

支持格式：ASS / SSA / SRT / VTT

## 技术栈

| 层 | 技术 |
|----|------|
| 后端 | Go + [pion/webrtc](https://github.com/pion/webrtc) v4 |
| 转码 | FFmpeg（VP8 实时编码 + Opus 音频） |
| 前端 | Vanilla JS + Vite + [JASSUB](https://github.com/ThaUnknown/jassub)（libass WASM） |
| 传输 | WebRTC（DTLS-SRTP 加密）+ WebSocket 信令 |
| 认证 | Argon2id + JWT（HS256） |
| 部署 | 单二进制 / Docker / docker-compose |

## 项目结构

```
syncwatch/
├── main.go                   # 入口
├── internal/
│   ├── config/               # YAML 配置加载
│   ├── auth/                 # Argon2 密码 + JWT
│   ├── media/                # FFmpeg 管道 + FFprobe + 字幕提取
│   ├── stream/               # RTP Reader + Jitter Buffer + Fan-out
│   ├── webrtc/               # WebRTC SFU（多 Peer 管理）
│   ├── signaling/            # WebSocket 信令 + 聊天
│   ├── room/                 # 播放状态机
│   └── api/                  # HTTP API + 中间件
├── web/                      # 前端（Vite 构建，嵌入二进制）
├── Dockerfile                # 多阶段构建
├── docker-compose.yml
└── config.example.yaml
```

## 许可证

MIT
