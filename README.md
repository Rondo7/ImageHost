# ImageHost

一个基于 Go + SQLite 构建的自托管图像管理平台。支持自动 WebP 转换、瀑布流布局 Web UI、基于标签的过滤以及公共随机图片 API。

---

## 部署方式

### 方式一：Docker Compose（推荐）

Docker 部署会自动构建镜像，并把图片文件和数据库持久化到当前目录的 `uploads/` 和 `data/`。

```bash
git clone <repo> imagehost
cd imagehost

# 首次部署前请修改 docker-compose.yml 里的 UPLOAD_PASSWORD
docker compose up -d --build
```

默认 Compose 配置会把容器内的 `8080` 映射到宿主机端口。请以 `docker-compose.yml` 里的 `ports` 为准，例如：

```yaml
ports:
  - "8080:8080"
```

上面的配置表示访问地址是：

```text
http://服务器IP:8080
```

常用命令：

```bash
# 查看运行状态
docker compose ps

# 查看日志
docker compose logs -f

# 修改代码或 Dockerfile 后重新构建
docker compose up -d --build

# 停止服务
docker compose down
```

### 方式二：VPS 直接运行二进制文件

适用于普通 `x86_64/amd64` Ubuntu VPS。先在 VPS 上确认架构：

```bash
uname -m
```

如果输出是 `x86_64`，使用 `imagehost-linux-amd64`。把以下内容放到同一个运行目录：

```text
imagehost-linux-amd64
frontend/
uploads/    # 可选；没有会自动创建
data/       # 可选；没有会自动创建
```

如果需要重新编译 Linux amd64 版本，可以在项目根目录执行：

```bash
mkdir -p dist
docker run --rm --platform linux/amd64 \
  -v "$PWD:/src" \
  -w /src \
  golang:1.22-bookworm \
  bash -lc 'export PATH=/usr/local/go/bin:$PATH; CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/imagehost-linux-amd64 .'
```

启动命令：

```bash
chmod +x imagehost-linux-amd64
UPLOAD_PASSWORD=请改成强密码 PORT=8080 ./imagehost-linux-amd64
```

后台运行示例：

```bash
nohup env UPLOAD_PASSWORD=请改成强密码 PORT=8080 ./imagehost-linux-amd64 > imagehost.log 2>&1 &
```

验证密码是否生效：

```bash
# 错误密码应返回 401
curl -i -H 'Authorization: Bearer wrong' \
  http://127.0.0.1:8080/api/v1/images?limit=1

# 正确密码应返回 200
curl -i -H 'Authorization: Bearer 请改成强密码' \
  http://127.0.0.1:8080/api/v1/images?limit=1
```

如果前面有 Nginx 反向代理，请把代理目标指向程序监听的端口，例如 `127.0.0.1:8080`。

### 方式三：源码运行

**要求：** Go 1.21+、GCC。项目使用了 cgo 依赖，Windows/Linux/macOS 都需要可用的 C 编译器。

Ubuntu/Debian：

```bash
sudo apt-get update
sudo apt-get install -y gcc
go mod tidy
UPLOAD_PASSWORD=请改成强密码 PORT=8080 go run .
```

macOS：

```bash
brew install go
go mod tidy
UPLOAD_PASSWORD=请改成强密码 PORT=8080 go run .
```

---

## 配置

启动时，服务器会读取（或创建） **`data/config.json`**：

```json
{
  "upload_password":  "admin123",
  "storage_dir":      "uploads",
  "data_dir":         "data",
  "port":             "8080",
  "random_rate_limit": 100,
  "auth_max_attempts": 10,
  "max_upload_mb": 50,
  "max_upload_count": 50,
  "resize_max_pixels": 4000,
  "reject_max_pixels": 10000
}
```

| Field | Default | Description |
|---|---|---|
| `upload_password` | `admin123` | 上传 / 删除 / 管理 API 的密码 |
| `storage_dir` | `uploads` | 图片文件存储目录 |
| `data_dir` | `data` | SQLite 数据库和配置文件目录 |
| `port` | `8080` | HTTP 服务器监听的 TCP 端口 |
| `random_rate_limit` | `100` | 每个 IP 每分钟访问 `/api/random` 的最大请求数（`0` = 禁用） |
| `auth_max_attempts` | `10` | 每个 IP 每分钟允许的最大认证失败次数，超过后进入静默冷却 |
| `max_upload_mb` | `50` | 单张图片最大上传大小（MB） |
| `max_upload_count` | `50` | 单次请求最多上传图片数量 |
| `resize_max_pixels` | `4000` | 转换 WebP 时，如宽或高超过该值则按原比例缩小（`0` = 禁用缩放） |
| `reject_max_pixels` | `10000` | 如宽或高超过该值则拒绝上传（`0` = 禁用分辨率拒绝） |

**环境变量覆盖**（优先级高于配置文件）：

| 变量名 | 覆盖字段 |
|---|---|
| `UPLOAD_PASSWORD` | `upload_password` |
| `PORT` | `port` |

修改 `data/config.json` 后需重启服务器以应用更改。

---

## API 参考

所有端点均以 `/api` 或 `/api/v1` 开头。受保护的端点需要：

```
Authorization: Bearer <upload_password>
```

或者使用查询参数 `?password=<upload_password>`。

---

### 公共端点

#### `GET /api/random` — 随机图片

默认直接返回图片文件（适用于 `<img src="...">` 标签）。

**查询参数**

| **参数**  | **类型** | **描述**                                   |
| --------- | -------- | ------------------------------------------ |
| `tag`     | string   | 按单个标签过滤                             |
| `tags`    | string   | 按多个标签过滤，逗号分隔（AND 逻辑）       |
| `format`  | string   | `webp` (默认) · `original` · `json`        |
| `json`    | `1`      | `format=json` 的简写                       |
| `count`   | integer  | JSON 模式下返回的图片数量 (1–50, 默认 `1`) |
| `exclude` | integer  | 要排除的图片 ID（避免连续重复）            |

**速率限制：** 每个 IP 每分钟 `random_rate_limit` 次请求。超出后返回 HTTP 429。

---

**示例 — 直接获取一张随机 WebP 图片（浏览器 / `<img>` 标签）**

```
GET /api/random
```

响应头包含：

```
X-Image-Id: 42
X-Image-Tags: nature,sunset
Cache-Control: no-store
```

------

**示例 — 避免重复（传入前一张图片的 ID）**

```
GET /api/random?exclude=42
```

------

**示例 — 按标签过滤**

```
GET /api/random?tag=nature
GET /api/random?tags=nature,sunset
```

------

**示例 — 获取原图而非 WebP**

```
GET /api/random?format=original
```

------

**示例 — JSON 响应（单张图片）**

```
GET /api/random?format=json
```

```json
{
  "images": [
    {
      "id": 42,
      "webp_url": "/uploads/webp/1712345678.webp",
      "orig_url": "/uploads/original/1712345678.jpg",
      "is_gif": false,
      "width": 1920,
      "height": 1080,
      "tags": ["nature", "sunset"],
      "created_at": "2024-04-06T12:00:00Z"
    }
  ],
  "count": 1
}
```

---

**实例 — JSON 响应（多张图片）**

```
GET /api/random?format=json&count=5
GET /api/random?format=json&count=3&tag=nature
```

```json
{
  "images": [ { ... }, { ... }, { ... } ],
  "count": 3
}
```

---

#### `GET /api/images` — 图片列表

```
GET /api/images?page=1&limit=30&tag=nature
```

**Query parameters**

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `page` | integer | `1` | 页码 |
| `limit` | integer | `50` | 每页数量（最大200张） |
| `tag` | string | — | 单标签 |
| `tags` | string | — | 多标签 |

**Response**

```json
{
  "images": [ { "id": 1, "webp_url": "...", "orig_url": "...", "is_gif": false, "width": 1920, "height": 1080, "tags": ["nature"], "created_at": "..." } ],
  "total": 142,
  "page": 1,
  "limit": 50
}
```

---

#### `GET /api/tags` — 标签列表

```
GET /api/tags
```

**响应**

```json
{
  "tags": [
    { "id": 1, "name": "nature", "count": 24 },
    { "id": 2, "name": "sunset", "count": 11 }
  ]
}
```

---

### 管理 API（需要认证）

所有管理端点均要求提供 `Authorization: Bearer <password>`.

---

#### `POST /api/v1/images` — 上传图片

通过 `multipart/form-data` 上传一张或多张图片。

```bash
# 上传单张图片
curl -X POST http://localhost:8080/api/v1/images \
  -H "Authorization: Bearer admin123" \
  -F "files=@photo.jpg"

# 上传并添加标签
curl -X POST http://localhost:8080/api/v1/images \
  -H "Authorization: Bearer admin123" \
  -F "files=@photo.jpg" \
  -F "tags=nature,sunset"

# 一次性上传多张图片
curl -X POST http://localhost:8080/api/v1/images \
  -H "Authorization: Bearer admin123" \
  -F "files=@photo1.jpg" \
  -F "files=@photo2.png" \
  -F "files=@animation.gif" \
  -F "tags=travel"
```

标签也可以作为查询参数传递：

```bash
curl -X POST "http://localhost:8080/api/v1/images?tags=nature,sunset" \
  -H "Authorization: Bearer admin123" \
  -F "file=@photo.jpg"
```

****

| 字段 | 描述 |
|---|---|
| `files` or `file` | 要上传的图片文件 (JPG, PNG, WebP, GIF) |
| `tags` | 逗号分隔的标签列表 |

- **图片处理**
  - **JPEG / PNG / WebP** → 转换为 WebP（质量 82）并保留原图
  - **GIF** → 保持原样（不转换）

**Response**

```json
{
  "results": [
    {
      "success": true,
      "id": 43,
      "filename": "photo.jpg",
      "webp_url": "/uploads/webp/1712345679.webp",
      "orig_url": "/uploads/original/1712345679.jpg",
      "tags": ["nature", "sunset"]
    }
  ]
}
```

错误响应（每个文件）：

```json
{
  "results": [
    { "success": false, "filename": "bad.bmp", "error": "unsupported format" }
  ]
}
```

---

#### `DELETE /api/v1/images/:id` —  删除图片

```bash
curl -X DELETE http://localhost:8080/api/v1/images/43 \
  -H "Authorization: Bearer admin123"
```

**Response**

```json
{ "success": true, "id": 43 }
```

---

#### `GET /api/v1/images` — 获取图片列表（管理）

与 `GET /api/images` 相同，但位于受身份验证保护的管理组下。接受相同的查询参数。

```bash
curl "http://localhost:8080/api/v1/images?page=1&limit=10" \
  -H "Authorization: Bearer admin123"
```

---

#### `GET /api/v1/images/:id` — 获取单张图片信息

```bash
curl http://localhost:8080/api/v1/images/43 \
  -H "Authorization: Bearer admin123"
```

**Response**

```json
{
  "id": 43,
  "webp_url": "/uploads/webp/1712345679.webp",
  "orig_url": "/uploads/original/1712345679.jpg",
  "is_gif": false,
  "width": 3840,
  "height": 2160,
  "tags": ["nature"],
  "created_at": "2024-04-06T12:00:00Z"
}
```

---

### 前端端点（需要认证）

这些端点由 Web UI 使用，但也可以作为常规 API 调用执行。

| 方法 | 路径 | 描述 |
|---|---|---|
| `POST` | `/api/upload` | 带有 SSE 进度的多部分上传 (fields: `files`, `tags`, `progress_id`) |
| `DELETE` | `/api/images/:id` | 删除图片 |
| `GET` | `/api/progress/:id` | 获取上传进度的 SSE 流 |

---

## 安全

### 身份验证

所有写入操作（上传、删除）都需要在 `data/config.json` 中设置 `upload_password`。在将服务公开暴露之前，请设置一个强密码：

```json
{ "upload_password": "my-very-strong-password-here" }
```

### 防暴力破解保护

失败的认证尝试会根据 IP 进行速率限制。如果一分钟内的失败次数达到 `auth_max_attempts`，该 IP 将进入静默冷却期 —— 它会继续收到 HTTP 401（而不是 429），因此其表现与输入错误密码时无法区分。

计数器会在一分钟后自动重置，或者在成功登录后立即重置。

### 随机图片速率限制

`/api/random` 受基于 IP 的滑动窗口计数器保护（每分钟 `random_rate_limit` 次请求）。超过限制的客户端将收到 HTTP 429 响应，并附带 `Retry-After: 60` 标头。 在配置中将 `random_rate_limit` 设置为 0 可完全禁用速率限制。

---

## 文件结构

```
imagehost/
├── main.go                  # 入口点，路由设置
├── config/config.go         # 配置加载与热更新
├── database/db.go           # SQLite 数据库表结构和查询
├── handlers/handlers.go     # HTTP 处理函数
├── middleware/ratelimit.go  # 速率限制与防暴力破解保护
├── storage/storage.go       # 文件 I/O 与 WebP 转换
├── frontend/index.html      # 单页 Web UI
├── data/
│   ├── config.json          # 运行时配置（自动创建）
│   └── imagehost.db         # SQLite 数据库（自动创建）
├── uploads/
│   ├── original/            # 原始上传文件
│   ├── webp/                # 转换后的 WebP 文件
│   └── gif/                 # GIF 文件（保持原样）
├── Dockerfile
└── docker-compose.yml
```

---

## 生产运行建议

### 密码配置优先级

`UPLOAD_PASSWORD` 环境变量优先级高于 `data/config.json`。生产环境建议优先使用环境变量：

```bash
UPLOAD_PASSWORD=请改成强密码 PORT=8080 ./imagehost-linux-amd64
```

如果不设置 `UPLOAD_PASSWORD`，程序会读取 `data/config.json`；首次运行时会自动创建默认配置。公开部署前务必修改默认密码。

### systemd 服务示例

直接运行二进制文件时，可以用 systemd 托管进程。假设项目目录是 `/root/app/imagehost`：

```ini
[Unit]
Description=ImageHost
After=network.target

[Service]
Type=simple
WorkingDirectory=/root/app/imagehost
Environment=UPLOAD_PASSWORD=请改成强密码
Environment=PORT=8080
ExecStart=/root/app/imagehost/imagehost-linux-amd64
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

保存为 `/etc/systemd/system/imagehost.service` 后执行：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now imagehost
sudo systemctl status imagehost
sudo journalctl -u imagehost -f
```

### Nginx 反向代理示例

如果需要通过域名访问，可以让 Nginx 转发到程序监听的本地端口：

```nginx
server {
    listen 80;
    server_name example.com;

    client_max_body_size 100m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### 更新部署

Docker 部署：

```bash
docker compose up -d --build
```

二进制部署：

```bash
sudo systemctl stop imagehost
cp imagehost-linux-amd64 /root/app/imagehost/imagehost-linux-amd64
chmod +x /root/app/imagehost/imagehost-linux-amd64
sudo systemctl start imagehost
```
