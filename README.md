# SyncWatch

[![Docker build](https://github.com/kiseding/syncwatch/actions/workflows/docker.yml/badge.svg)](https://github.com/kiseding/syncwatch/actions/workflows/docker.yml)

SyncWatch 是一个单 Host 的私有同步观影服务。Host 选择服务器媒体、上传文件或输入网络视频 URL，Viewer 在浏览器登录后会同步播放状态、进度、倍速、音轨和字幕。

## 功能

- Host 控制播放、暂停、跳转、倍速和强制同步
- 本地媒体库、浏览器上传、HTTP(S) 视频 URL 和 HLS/m3u8
- ASS/SSA/SRT/VTT 外挂字幕，支持自动发现和浏览器上传
- HLS 多音轨切换；普通视频在浏览器提供 `audioTracks` API 时支持多音轨
- WebSocket 实时同步、自动重连和房间聊天
- Viewer 音量、静音、全屏和移动端布局
- Argon2id 密码哈希、JWT 角色鉴权、登录限流和媒体目录边界校验
- PWA、TLS、Docker 和单二进制部署

> 媒体是否能直接播放取决于浏览器支持的容器与编码。兼容性最好的是 H.264/AAC MP4、WebM 和 HLS。MKV/AVI 可以被媒体库识别，但浏览器不支持其中的编码时需要事先转码。

## 直接运行

要求 Go 1.25+、Node.js 22+。FFmpeg/FFprobe 用于媒体信息和音轨检测；没有安装时基础播放仍可使用。

```bash
cp config.example.yaml config.yaml
# 修改 config.yaml 的密码、媒体目录和上传目录
make build
./syncwatch --config config.yaml
```

仓库包含已构建的前端，因此也可以直接执行：

```bash
go build -o syncwatch .
./syncwatch --config config.yaml
```

访问地址：

- Viewer：`http://服务器地址:8080/`
- Host：`http://服务器地址:8080/admin`
- 健康检查：`http://服务器地址:8080/healthz`

未提供配置文件时服务也能启动，默认 Viewer/Host 密码均为 `syncwatch`。正式部署必须修改密码并设置固定的 `jwt_secret`。

## Docker

```bash
mkdir -p media data
cp config.example.yaml data/config.yaml
# 修改 data/config.yaml，至少设置 auth.password 和 auth.admin_password
docker compose up -d --build
```

Compose 将 `./media` 只读挂载到 `/media`，将 `./data` 挂载到 `/data`。示例配置已经使用这两个容器路径。

## 配置

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  tls: false
  cert_file: ""
  key_file: ""

media:
  scan_dirs:
    - "/media"
  allowed_extensions:
    - ".mp4"
    - ".mkv"
    - ".avi"
    - ".mov"
    - ".webm"
  upload_dir: "/data/uploads"

auth:
  password: "viewer-password"
  admin_password: "host-password"
  rate_limit_per_min: 5
  session_timeout: 86400
  jwt_secret: "请替换为足够长的随机字符串"
```

也可以用 `password_hash` 和 `admin_password_hash` 提供预生成的 Argon2id 哈希。明文密码只在启动时用于生成内存中的哈希，不会写回配置文件。

## 字幕

与视频同目录、同基础文件名的字幕会自动发现：

```text
movie.mp4
movie.ass
movie.zh-CN.srt
```

上传字幕后，当前房间会立即切换到该字幕；后来加入或重连的 Viewer 也会收到完整字幕状态。

## 开发与验证

```bash
make frontend       # 构建 web/dist
make test           # 构建前端并运行 Go 测试
make build-release  # 静态发布二进制
```

前端开发服务器：

```bash
cd web
npm ci
npm run dev
```

## 许可证

MIT
