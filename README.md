# SyncWatch — 私人异地同步观影

单 Host 模式的私人异地同步观影系统。

Host 选择影片，Viewer 通过浏览器输入密码即可同步观看。所有 Viewer 看到完全一致的播放进度、音轨和字幕。

## 特性

- 🎬 **一人播放，多人同步** — 播放 / 暂停 / 进度 / 倍速完全一致
- 🔗 **HTTP 直链传输** — 视频文件直接 HTTP 分发，支持 HLS/m3u8 / MP4 / WebM
- 📝 **完整字幕支持** — ASS/SSA 特效（描边、阴影、字体样式），SRT/VTT
- 🎵 **多音轨** — 自动检测、一键切换
- ⚡ **倍速播放** — 0.5x / 1x / 1.25x / 1.5x / 2x
- 💬 **在线聊天** — 文本消息 + 系统通知
- 📱 **PWA** — 手机/桌面安装，全屏运行，响应式布局
- 🔒 **安全** — Argon2 密码哈希、JWT 鉴权、登录限流
- 🐳 **单二进制部署** — ~13MB 静态编译，支持 Linux / Docker / NAS

## 快速开始

### 直接运行

```bash
# 编译
go build -o syncwatch .

# 创建配置
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
# 编辑 data/config.yaml，设置密码

docker compose up -d
```

## 配置

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  public_url: ""     # 公网地址，用于生成本地文件访问 URL

media:
  scan_dirs:                # 本地媒体文件扫描目录
    - "/home/user/media"
  allowed_extensions:       # 支持的视频格式
    - ".mp4"
    - ".mkv"
    - ".avi"
    - ".mov"
    - ".webm"
  upload_dir: "/tmp/syncwatch_uploads"

auth:
  password: ""              # 观影密码（明文，自动转为 Argon2 哈希）
  admin_password: ""        # 管理密码（不填则共用观影密码）
  # password_hash: "$argon2id$..."   # 或直接填预生成哈希
  rate_limit_per_min: 5
  session_timeout: 86400    # 24 小时
  jwt_secret: ""            # JWT 密钥（留空自动生成）
```

## 使用说明

### Host（管理者）

1. 打开 `http://host-ip:8080/admin`
2. 输入管理密码，进入控制台
3. 选择本地文件 / 上传视频 / 输入视频 URL
4. 控制播放：Space 暂停、←→ 快退快进

管理功能：
- 播放 / 暂停 / 跳转进度
- 切换音轨 / 字幕
- 调整倍速（0.5x–2x）
- 上传视频文件、字幕文件
- 支持网络视频 URL 和本地文件

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
| 后端 | Go |
| 媒体分析 | FFprobe（可选，用于音轨/字幕检测） |
| 前端 | Vanilla JS + Vite + [JASSUB](https://github.com/ThaUnknown/jassub)（libass WASM） |
| 传输 | HTTP ServeFile + WebSocket 信令 + HLS.js |
| 认证 | Argon2id + JWT（HS256） |
| 部署 | 单二进制 / Docker / docker-compose |

## 项目结构

```
syncwatch/
├── main.go                   # 入口
├── internal/
│   ├── config/               # YAML 配置加载
│   ├── auth/                 # Argon2 密码 + JWT + 限流
│   ├── media/                # FFprobe 探测 + 字幕提取
│   ├── signaling/            # WebSocket 信令 + 聊天
│   ├── room/                 # 播放状态管理
│   └── api/                  # HTTP API + 中间件
├── web/                      # 前端（Vite 构建，嵌入二进制）
├── Dockerfile                # 多阶段构建
├── docker-compose.yml
└── config.example.yaml
```

## 许可证

MIT
