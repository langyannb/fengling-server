# goapi —— 风铃分享库 Go 网关（阶段 0 + 阶段 1）

监听 `127.0.0.1:9100`，**原生**接管 4 个入口，其余全部 action **原样透传**给现有 PHP：

| 入口 | 处理方式 |
| --- | --- |
| `?action=health` | Go 原生（新增能力，PHP 没有） |
| `?action=version` | Go 原生（与 PHP 逐字节一致） |
| `?action=stream` | Go 原生 SSE（逐字节对齐 `api.php:3648 sse_run()`） |
| `POST /ws`（`location = /ws`） | Go 原生 WebSocket（新增能力） |
| `?action=user_avatar` / `social_image_upload` / `social_video_upload` / `upload` / `upload_apk` / `video_config` | Go 原生（阶段 1，见下文「阶段 1」章节） |
| `GET /healthz` | Go 原生（阶段 1，仅本机可达） |
| 其它任意 action | 透传 PHP-FPM（`unix:/tmp/php-cgi-85.sock`），状态码/响应头/响应体逐字透传、流式不缓冲 |

目标只有一条：**客户端零改动**。

## 目录

```
main.go                     进程入口（配置/依赖组装/优雅关闭）
internal/config/            环境变量 + 安全默认值
internal/store/store.go     MySQL 连接池 + token→user + require_admin 语义
internal/store/redis.go     Redis（在线状态 / 限流 / Pub-Sub 封装，可降级）
internal/httpapi/ctx.go     Ctx：action/参数/用户（复刻 PHP param() 顺序）
internal/httpapi/shell.go   json_out / json_error 的逐字对齐实现
internal/httpapi/router.go  分发：原生优先，否则透传
internal/httpapi/native_health.go
internal/httpapi/native_version.go
internal/httpapi/phpfpm.go  gofast FastCGI 客户端（每次请求一条连接 + 每次 Write 后 Flush）
internal/httpapi/native_upload.go  阶段 1：6 个上传/配置 action 的 handler
internal/upload/image.go           阶段 1：图片格式判定 + compress_image 复刻
internal/upload/video.go           阶段 1：视频容器判定 + 参数兜底
internal/upload/multipart.go       阶段 1：流式 multipart（禁 ParseMultipartForm）
internal/upload/s3.go              阶段 1：S3 SigV4 客户端（stdlib 手写）
internal/upload/key.go             阶段 1：s3_key / apk key / 扩展名
internal/jobs/video_cleanup.go     阶段 1：视频清理纯函数 + 常驻 ticker
internal/store/video.go            阶段 1：video_config 解析 + 待清理行 SQL
internal/realtime/hub.go    连接注册表 + 1 秒轮询（SSE 自循环 + WS 全局轮询泵/环缓；阶段 3/4 换成 Redis Pub/Sub 的替换点）
internal/realtime/sse.go    原生 SSE（自己查库、同步写响应体，逐字对齐 PHP）
internal/realtime/ws.go     原生 WebSocket（事件由 hub 的全局轮询泵投递到 conn.send）
internal/realtime/engine.go 引擎装配
internal/phpjson/           **PHP 兼容 JSON 编码器**（见下）
deploy/                     systemd 单元 / nginx 片段 / 幂等安装脚本
tools/goapi_diff.sh         双跑逐字对比
```

### 为什么要有 `internal/phpjson`（契约文件清单之外的唯一新增包）

`encoding/json` 与 PHP `json_encode($v, JSON_UNESCAPED_UNICODE)` 有四处在**字节层面**不同，
而客户端对这些字节是敏感的（它按 PHP 的输出写过解析与缓存）：

1. **`/` 的转义**：PHP 默认把 `/` 写成 `\/`（`api.php` 没传 `JSON_UNESCAPED_SLASHES`），
   `encoding/json` 永远输出裸 `/` —— 例如 `https:\/\/fenglin.cn-nb1.rains3.com\/apk\/x.apk`。
2. **键序**：PHP 是数组插入顺序，Go 的 `map` 会被排序（所以本包用有序对象 `O`，不用 map）。
3. **浮点形态**：PHP 是 `serialize_precision=-1` 语义，**整数浮点输出 `12.0` 而不是 `12`**；
   `version` 的 `size_mb` 在 PHP 里是 `(float)` 强转，所以这条是真实差异。
4. **HTML 转义**：`encoding/json` 默认把 `<` `>` `&` 转成 `\u003c` 等，PHP 不转
   （本包用 `SetEscapeHTML(false)`）。

原生 action（version/health/SSE 事件/WS 帧）全部走这个包，才能做到逐字节一致。

## 环境变量（systemd 由 `/etc/fengling/goapi.env` 注入）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `GOAPI_ADDR` | `127.0.0.1:9100` | 监听地址（只回环） |
| `LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `FPM_NETWORK` / `FPM_ADDR` | `unix` / `/tmp/php-cgi-85.sock` | 透传目标 |
| `PHP_SCRIPT` / `PHP_DOCROOT` / `PHP_SCRIPT_NAME` | `/www/wwwroot/flfxk/api.php` / `/www/wwwroot/flfxk` / `/api.php` | CGI 参数 |
| `DB_SOCKET` | `/tmp/mysql.sock` | **MySQL 走 unix socket**（见下） |
| `DB_HOST` / `DB_PORT` | `localhost` / `3306` | 仅当 `DB_SOCKET` 为空时回落 TCP |
| `DB_NAME` / `DB_USER` / `DB_PASS` / `DB_CHARSET` | `flfxk` / `flfxk` / 空 / `utf8mb4` | 与 `config.php` 一致 |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | `127.0.0.1:6379` / 空 / `0` | 非 0 库会被强制回退到 0 |
| `POLL_INTERVAL` / `SSE_HEARTBEAT` / `SSE_MAX_AGE` | `1s` / `10s` / `25s` | 与 PHP `sse_run` 一致，别改 |
| `WS_HEARTBEAT` / `WS_IDLE` / `WS_WRITE_WAIT` / `WS_SEND_BUFFER` | `15s` / `60s` / `10s` / `64` | WebSocket |
| `ONLINE_TTL` | `90s` | Redis 在线标记 TTL（心跳 10s 续期） |
| `S3_ENDPOINT` / `S3_BUCKET` / `S3_REGION` | 无（**必填**） | 雨云对象存储（`/etc/fengling/goapi.env`，0600） |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` / `S3_PUBLIC_URL` | 无（**必填**） | 凭据与公共读前缀；缺任一项 goapi 拒绝启动 |
| `UPLOAD_TMP_DIR` | 空 = `os.TempDir()` | 上传临时目录（`/tmp`，`PrivateTmp=no`） |
| `UPLOAD_RATE_LIMIT` / `UPLOAD_RATE_WINDOW` | `30` / `10m` | 每人每窗口上传次数（0 = 关闭；Redis 挂 fail-open） |
| `VIDEO_CLEANUP_INITIAL` / `VIDEO_CLEANUP_INTERVAL` | `30s` / `10m` | 视频清理 ticker 首次延迟与周期 |

**启动即校验**：`GOAPI_ADDR`/`DB_NAME`/`DB_USER`/`DB_PASS`/`REDIS_ADDR`/`S3_*` 缺失，或
`DB_SOCKET` 与 `DB_HOST` 同时为空，都会让 goapi **拒绝启动**（契约第 5 节：不许带默认值跑到线上）。

## 逐字对齐要点（改动前务必先读）

- **响应壳**：`{"code":…,"msg":…,"data":…}`，`Content-Type: application/json; charset=utf-8`，
  无缓存时 `Cache-Control: no-store`；`json_error` 的 `data` 恒为 `null`（不是 `{}`/`[]`）。
- **HTTP 状态码映射**（`config.php:50-59`）：`0→200`、`401→401`、`404→404`、`500→500`、
  **其它非 0 → 400**。注意 `403`（`require_admin` 的「没有权限」）走的就是 **HTTP 400**，
  只是 JSON 里的 `code` 是 `403` —— 契约文档早期那句「401/403 都是 HTTP 400」是错的。
- **gzip 由后端自己压**：`Accept-Encoding` 里含 `gzip`（大小写敏感子串，同 PHP `strpos`）就
  gzip 并加 `Content-Encoding: gzip`；gzip 头对齐 PHP（level 6、mtime=0、OS=3）。
  deflate 流不保证与 zlib 逐字节相同（**解压后**一定相同）。nginx 侧因此 `gzip off`。
- **CORS**：原生 action、`OPTIONS`（→204）、以及**所有由 Go 直接生成的错误响应**
  （`gatewayError`、`请求体过大`、`读取请求体失败`）都发三头；**透传成功路径一个头都不预设**，
  因为 PHP 自己会发，重复设置会出现重复响应头。
- **错误文案**：原生 action / 网关兜底的 `msg` 里**绝不出现 Go 的文件名或行号**
  （PHP 也从不对客户端暴露路径），一律 `服务器错误: <原因>`，细节只写进结构化日志。
  例如 `version` 读库失败 → `500 {"code":500,"msg":"服务器错误: dial unix …","data":null}`。
- **未知 action 不自己回 404**，一律透传，由 PHP 回 `404 未知操作: xxx`。
- **SSE**：先 `retry: 2000\n\n`（**不**立刻发心跳），首个 `: hb\n\n` 在第 10 秒；
  10 秒心跳、25 秒封顶（`event: bye\ndata: {"reason":"timeout"}`）；游标 `pm_id/group_id <= 0`
  = 取当前 `MAX(id)`（绝不重放历史）；每条事件立即 flush。
  `X-Accel-Buffering: no` 照发，但 nginx 会把它吃掉（客户端看不到，属正常）。
  **客户端断开时靠 `r.Context().Done()` 立刻返回并停止轮询**，不像 PHP 那样白占 worker 到 25 秒。
- **兼容性坑（照抄 PHP，不要「顺手修好」）**：`at_users` 用 `(int)trim($v)` 解析，
  所以结尾多一个逗号（空段）也会把 `at_all` 置 1；`group` 事件没有 `username` 字段；
  group 查询**不按群成员过滤**。
- **WebSocket**：未登录先升级再发 `{"type":"error","code":401,"msg":"登录已失效"}` 并以 4001 关闭；
  `hello` → `pm/group`（字段与 SSE 完全一致）→ 15s `ping` → 收 `ping` 回 `pong`；
  客户端 `cursor` 帧语义同 SSE；**不做 25 秒封顶**；发送缓冲 64 满则丢弃并计数（不阻塞 hub）。
  60 秒无任何入站帧才关（关闭码 4002，契约未规定该码，客户端应把它当作可重连）。

## Redis / MySQL 的生产纪律

- Redis 是**共用实例**（同机还跑着另一个站点的 WordPress 对象缓存，`maxmemory 0` +
  `noeviction`）：所有键与 Pub/Sub 频道统一前缀 **`fls:`**，**禁止 FLUSHDB/FLUSHALL/KEYS**，
  只允许库 0，所有键都带 TTL。Redis 不可用时全部降级（fail-open），不 panic、不卡请求。
  在线状态：`SADD fls:online <uid>` + `SETEX fls:online:<uid> 90 <now>`，集合键本身也续 TTL；
  限流键 `fls:rl:<kind>:<uid>`（INCR + EXPIRE，本单只提供封装 + 单测）。
- MySQL **走 unix socket**（`DB_SOCKET`，对齐 PDO 里 `DB_HOST='localhost'` 的行为与
  `'user'@'localhost'` 授权）。驱动**不开 `parseTime`**：开了之后 `DATETIME` 会变成 `time.Time`，
  透传给客户端就成了 RFC3339（PHP 给的是 `2026-10-05 15:29:24`），且会按 `loc` 平移时区。
  不开的话驱动原样返回 MySQL 字符串，天然逐字一致；阶段 2 若确实要 `time.Time`，
  请用 `store.FormatDateTime()` 转回 PHP 格式再输出。连接池 32/16、`ConnMaxLifetime 30m`。

## 网关侧两项加固

1. **客户端 IP**：`ClientIP()` 顺序 = `X-Real-IP`（我们 nginx 设的、可信）→ `X-Forwarded-For`
   （**整串原样**，不切第一个，与 PHP `client_ip()` 一致）→ `RemoteAddr`。
   透传时 `REMOTE_ADDR` 用它（否则 Go 前面是 nginx，PHP 只会看到 `127.0.0.1`）。
   nginx 片段因此**刻意不设置 `X-Forwarded-For`**（用 `$proxy_add_x_forwarded_for` 会追加、
   破坏逐字一致），只设 `X-Real-IP`。
2. **请求体长度**：生产路径 nginx 是 `proxy_request_buffering on`，上游一定有 `Content-Length`；
   但直连 9100 时可能是 chunked，此时先把 body 读进内存（**上限 16MB**，超了回
   `400 {"code":1,"msg":"请求体过大","data":null}`），再把 `CONTENT_LENGTH` 填成真实长度，
   否则 php-cgi 会把 POST 体当空、参数静默丢失。有 `Content-Length` 时**完全不缓冲**，
   保证 150m 大上传的内存占用与迁移前一致。

## 阶段 1：上传接口 Go 原生（流式）+ 视频清理常驻 ticker

阶段 1 在**同一分支 `feature/goapi`、同一 `goapi/` 目录**里新增 6 个原生 action 与 1 个后台任务；
`api.php` 一字节未改，继续作为对照与回滚退路。

| action | 认证 | 上限 | 关键行为 | 返回 |
| --- | --- | --- | --- | --- |
| `user_avatar` | 登录 | 10MB | `compress_image(512, 90)`，key `avatars/…`，回写 `users.avatar` | `{url}` |
| `social_image_upload` | 登录 + `is_active=1` | 10MB | `compress_image(1600, 82)`（不收 GIF），key `chat/…` | `{url,width,height}`（**压缩前**原始尺寸） |
| `social_video_upload` | 登录 + `is_active=1` + 视频开关 | `video_config.max_mb` | 容器判定 → 参数校验 → 流式 PUT → 顺手清理一次 | `{url,size,width,height,duration,cleaned}` |
| `upload` | 管理员 | 200MB(apk)/10MB(图片) | `type=apk` 走 APK 分支，否则图片分支（gif 不压缩） | `{url,filename,size}` |
| `upload_apk` | 管理员 | 200MB，仅 `.apk` | 流式 PUT + `x-amz-content-sha256: UNSIGNED-PAYLOAD`，key `apk/fengling_Ymd_His.apk` | `{url,size}` |
| `video_config` | 无需登录 | — | 只读 `settings.video_config` 的 `enabled`/`max_mb` | `{enabled,max_mb}` |

另新增最小运维端点 `GET /healthz`（展示最近一次视频清理时间；nginx 只反代 `/api.php` 与 `/ws`，
所以它仅在 `127.0.0.1:9100` 上可达），并把 `OPTIONS` 的 204 响应补上 PHP 的默认
`Content-Type: text/html; charset=UTF-8`（阶段 0 遗留的最后一处头差异）。

新增文件：

```
internal/httpapi/native_upload.go  6 个 action 的 handler（分发入口、鉴权、文案）
internal/upload/image.go           图片格式判定（getimagesize 语义）+ compress_image 复刻
internal/upload/video.go           video_detect_ext / video_ctype / video_meta_params 复刻
internal/upload/multipart.go       流式 multipart（file 部件落临时文件，禁 ParseMultipartForm）
internal/upload/s3.go              S3 SigV4 客户端（stdlib 手写，path-style）
internal/upload/key.go             s3_key / apk key / 文件名取扩展名
internal/jobs/video_cleanup.go     视频清理纯函数 + 常驻 ticker
internal/store/video.go            video_config 解析 + 待清理行读取/落地 UPDATE
```

### 为什么要流式解析 multipart

Android 客户端（`ApiClient.kt` 的 `socialVideoUpload`）**先发 `file` 部件，之后才发
`video_w/video_h/video_duration/video_size` 四个文本部件**。也就是说：不可能「先读参数再决定收不收文件」。
`r.ParseMultipartForm` 会把超大部件缓冲到内存或它自己管理的临时文件（不可控，且会先吃掉整个 body），
所以这里统一用 `r.MultipartReader()`：

1. `file` 部件边读边写临时文件（`os.CreateTemp`，0600，默认 `os.TempDir()` 即 `/tmp`；单元是 `PrivateTmp=no`，与 php-cgi 共用）；
2. 同时算 `sha256`（后面直接当 SigV4 的 payload hash 用，不用读第二遍）；
3. 超过该 action 的上限就只多写 1 字节并停止（调用方用 `size > max` 复刻 PHP 的判断与文案）；
4. 继续读完剩余文本部件 → 全部校验通过 → 再把临时文件流式 PUT 到 S3；
5. 临时文件一律 `defer os.Remove`。峰值内存 O(1)。

### 图片压缩：与 PHP 的差异（**不是逐字节一致**）

PHP 的 `compress_image` 在**不需要缩放时也会无条件重编码**。Go 侧没有纯 Go 的 lossy WebP 编码器
（`chai2010/webp`、`kolesa-team/go-webp` 都要 cgo，会破坏 `CGO_ENABLED=0` 静态编译），所以按契约附录 C：

| 场景 | PHP | Go |
| --- | --- | --- |
| 宽 <= maxWidth | 解码 + 按扩展名重编码 | **原样上传原始字节**（扩展名/MIME 不变） |
| 像素 > 6000 万 | 原样 | 原样（同） |
| 解码失败 | 原样 | 原样（同） |
| 宽 > maxWidth 且源无 alpha | 按扩展名编码（webp 编 WebP、png 编 PNG、gif 编 PNG） | **编 JPEG**（质量用该接口的 quality），扩展名/MIME → `jpg`/`image/jpeg` |
| 宽 > maxWidth 且源有 alpha | WebP/PNG 保透明 | **编 PNG**，扩展名/MIME → `png`/`image/png` |

后果：**上传 GIF（头像）或 WebP 且需要缩放时，key 的扩展名与 Content-Type 会从 `gif`/`webp` 变成
`png`/`jpg`**（客户端拿到的是随机 URL，扩展名不可见）；PNG 压缩级别与 GD 的 7 不同、JPEG 编码器也不同，
**像素尺寸一致但字节不同**。不需要缩放的图片（绝大多数头像/群图）是原样字节上传，画质只会更好、体积可能更大
（上限本来就是 10MB）。

另一处已知差异：`getimagesize` 能识别 BMP/TIFF/PSD/ICO，PHP 会走到白名单分支报「只支持 … 图片」；
Go 没有这些解码器，用文件头识别后返回**同一句白名单文案**，不会退化成「这不是一张有效的图片」。

### S3 客户端：为什么手写 SigV4，而不是 minio-go / aws-sdk-go-v2

契约第 5 节允许二选一，这里**刻意用标准库手写**（`internal/upload/s3.go` 约 250 行），理由：

1. PHP 的签名范围**只有 3 个头**（`host;x-amz-content-sha256;x-amz-date`）、path-style、
   `Content-Type` 不参与签名。SDK 会自己加 `x-amz-*` 头、做 region 探测、可能走 virtual-host 或
   分片上传（`CreateMultipartUpload`），要逐条掰回 PHP 的行为反而更容易出偏差；
2. `upload_apk` 必须是**单次流式 PUT + `UNSIGNED-PAYLOAD`**，SDK 的默认路径是「先读 body 算 hash」或分片；
3. 避免为 30 行签名逻辑再拉一棵依赖树进 `CGO_ENABLED=0` 的静态二进制。

签名正确性用**已知答案向量**锁住：`internal/upload/s3_test.go` 里按 `api.php:57-77` 的公式用 Python
独立实现算出的 `Authorization` 与 Go 实现逐字比较（PUT / DELETE / `UNSIGNED-PAYLOAD` 三种），并断言
path-style 路径 `/<bucket>/<key>`、`x-amz-date`、payload hash。对象键的字符集被限制为
`[A-Za-z0-9._/-]`（生成的键只可能是 `<dir>/<YmdHis>_<8hex>.<ext>`），保证「签名用的 path」与
「实际请求 path」完全一致。

凭据只从 `/etc/fengling/goapi.env`（0600）读，**不进仓库、不进日志**；`S3_ENDPOINT/S3_BUCKET/S3_REGION/
S3_ACCESS_KEY/S3_SECRET_KEY/S3_PUBLIC_URL` 缺任何一个，`main` 直接拒绝启动。
`internal/upload/s3_test.go` 里用的是假值。

### 上传限流与降级（新增行为）

- 契约第 6 节要求「每人每 10 分钟最多 30 次上传」，超限回 `上传太频繁, 请稍后再试`（code=1）。
  **这是相对 PHP 的新增行为**，所以做成可配：`UPLOAD_RATE_LIMIT`（默认 30，置 0 关闭）、
  `UPLOAD_RATE_WINDOW`（默认 10m）。双跑对比若要跑几十次上传，把它置 0 或调大即可。
- Redis 挂了：限流 `INCR` 失败 → **fail-open 放行**；清理照跑，只是不写时间戳。绝不因为 Redis
  影响上传。
- MySQL 挂了：`video_config` 回落默认值、清理任务跳过本次；上传里的鉴权/回写失败按 PHP 语义走
  500 或 401。

### 视频清理常驻 ticker

`internal/jobs/video_cleanup.go`，语义与 `api.php:4058 video_cleanup(true, true, true)` 对齐：

- 取行：`social_messages` 再 `social_pm_messages`，`WHERE video IS NOT NULL AND video <> ''`
  `ORDER BY created_at ASC, id ASC`，两表结果**先群后私聊**拼成一个切片（决定删除顺序）；
- 无行直接返回；`respectAutoClean && auto_clean != 1` → `skipped='auto_clean=0 只统计不删除'` 且不动数据；
- 容量：`total_limit_mb*1024*1024`，**遍历中递减 total**（所以最旧优先删到不超限）；
- 天数：`keep_days>0` 时 `deadline=now-keep_days*86400`，`strtotime` 失败（ts<=0）的行**永不被按天数删**；
- 落地：先 `KeyFromURL`（只认本站公共读前缀）→ 删 S3 对象（失败忽略、不阻断）→
  `UPDATE t SET video = '', content = CONCAT(content, '[视频已清理]') WHERE id = ?`
  （**空字符串不是 NULL**、无分隔符不判重、`video_size` 保留审计）→ 成功才 `deleted++/freed_bytes += size`；
- 时序：启动后 `VIDEO_CLEANUP_INITIAL`（默认 30s）跑第一次，之后每 `VIDEO_CLEANUP_INTERVAL`
  （默认 10m）一次；只有 `deleted>0` 打 info，其余 debug；
- 健壮性：单次执行 `recover()` 兜 panic（不拖垮进程、不影响后续周期）；`ctx` 取消或
  `Cleaner.Stop()` 立刻退出（`main` 在 `engine.Close()` 之前先 `stopJobs()` + `cleaner.Stop()`，
  保证不再往已关闭的 Redis 客户端写键）；MySQL/Redis 不可用只记日志。
- Redis：完成后写 `goapi:video:last_cleanup`（契约 §6 指定）**并同时写** `fls:video:last_cleanup`
  （阶段 0 附录 A 的「所有键带 `fls:` 前缀」纪律），两者都是 24h TTL。

上传成功后仍会调一次同一份 `Cleanup()`（等价 `video_cleanup_if_needed()`），响应里的 `cleaned`
就是它的 `deleted`。

### 迁移与重启（**上线必读**）

1. 阶段 1 的二进制上线后**必须重启 goapi**（`systemctl restart fengling-goapi`）：新增了 6 个 action、
   `/healthz`、以及常驻 ticker；不重启则这些能力不生效（老进程仍会把 6 个 action 透传给 PHP，
   行为正确但没有 native 收益）。
2. **不需要一次性数据订正**。历史对象的 key 形态与本次新生成的一致（都是 `<dir>/<YmdHis>_<8hex>.<ext>`，
   清理逻辑按 URL 反推 key，与键是谁写的无关）；唯一会影响形态的是「需要缩放的 webp/gif 图片」，
   它只影响**新上传**的对象，不会回头改老对象。
3. 回滚：把 nginx 片段摘掉 + reload 即可回到纯 PHP（ticker 随 goapi 一起停），
   期间不会有任何「Go 才有的数据」残留（只多两条带 TTL 的 Redis 观测键）。

### 阶段 1 已知限制与未本地验证项

- **没有与真实 PHP 双跑过**：本机没有 PHP/MySQL/Redis/S3，`go test` 只覆盖纯函数、multipart 流式
  解析、S3 签名已知答案、ticker 生命周期；**真实上传/下载/尺寸校验由主 agent 在服务器上跑**
  （`/root/fls-goapi-upload.py`）。
- **WebP 解码路径没有 happy-path 单测**：本机没有纯 Go 的 WebP 编码器，造不出合法 webp 样本；
  只测了「畸形 webp → 这不是一张有效的图片」（与 `getimagesize` 的失败路径一致）。
  建议在服务器双跑时补一张真实 webp。
- `upload_apk` 的 `$_FILES['file']['error']`（`上传错误: N`）在 Go 里没有等价物：nginx 在
  `client_max_body_size` 超限时会直接回 413，请求根本到不了 goapi；multipart 中途截断会回
  `读取文件失败`。这条在正常路径上不可达。
- 视频的 `finfo` 分支没有复刻（Go 无 finfo）：真实 mp4/mov/mkv/webm 的文件头与 finfo 结论一致，
  差别只可能出现在「finfo 认得但文件头不符合任何已知容器」的畸形文件上。
- 上传失败时 Go 侧文案是 `上传失败, 请稍后重试`（图片/视频）与 `上传对象存储失败`（管理员 upload）、
  `上传对象存储失败 (HTTP N)`（APK），已逐字对齐；但**网络层错误**时 PHP 的 curl 状态码是 0，
  Go 也会输出 `(HTTP 0)`。
- 临时文件落在 `/tmp`：`admin upload` 的硬上限是 200MB+1，`.apk` 是 200MB —— 与 PHP 一样要占一份
  磁盘空间（systemd 单元是 `PrivateTmp=no`，`/tmp` 是宿主的）；如果服务器 `/tmp` 偏小要留意。
- 阶段 0 的一条差异仍未消：`version` 在 gzip 路径下的 deflate 字节。

## 部署

```bash
# 1) 取 CI 产物
#    GitHub Actions -> goapi 构建与测试 -> Artifacts -> goapi-linux-amd64
# 2) 上传到服务器, 和 deploy/ 一起放在同一目录, 然后:
sudo ./deploy/install.sh --binary ./goapi-linux-amd64 \
     --nginx-ext-dir /www/server/panel/vhost/nginx/extension/<站点名>
```

`install.sh` 是**幂等**的：备份（`/root/goapi-backup/`）→ 装二进制到 `/opt/fengling/goapi` →
生成 `/etc/fengling/goapi.env`（600，从 `config.php` 抓 `DB_*`、从 `api.php` 抓 `S3_*`）→
`daemon-reload` + `enable --now` → 写 nginx 片段 → **`nginx -t` 通过才 reload**（失败自动还原）→
打印健康检查。

> ⚠️ 如果主 vhost 里已有 `location = /api.php`，nginx 会报
> `duplicate location "/api.php"`：请先注释掉主 vhost 里那一行（install.sh 已备份 vhost）。

### 一键回滚

```bash
sudo ./deploy/install.sh --rollback
# 等价于: 删掉扩展目录里的 goapi.conf -> nginx -t && reload -> stop/disable 服务
```

回滚后**全站立刻回到纯 PHP**（主 vhost 的 `location ~ \.php$` 重新接管 `/api.php`）；
二进制与 env 保留，便于再次切换。手工回滚同理：删片段 + `nginx -t && /etc/init.d/nginx reload`。

## 双跑逐字对比

```bash
# PHP 基准 = 127.0.0.1:9101 (纯 PHP 对照端点), Go = 127.0.0.1:9100
./tools/goapi_diff.sh
TOKEN=<登录 token> ./tools/goapi_diff.sh --only version,apps,stream
```

对比每个 action 的状态码 / 响应头 / 响应体（字节级 `cmp`）以及 SSE 前 2 秒输出，
并额外验证 gzip 路径（两侧都压缩且解压后一致）。默认只跑无副作用的只读 action，
**不含任何写库或会扣抽奖次数的接口**。被忽略的响应头只有传输层/易变项：
`date/server/connection/keep-alive/transfer-encoding/content-length/vary/x-powered-by`。

## 本地自证

```bash
cd goapi
gofmt -l .
go vet ./...
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$HOME/goapi-linux-amd64" .
file "$HOME/goapi-linux-amd64"   # ELF 64-bit LSB executable, x86-64, statically linked
```

## 已知限制与风险

- **未与真实 PHP 双跑对比过**：本机无 PHP、无 Redis、无 MySQL，所有验证都是本地编译 + 单测；
  `tools/goapi_diff.sh` 与线上的 38 用例双跑由主 agent 在服务器上执行。
- gzip 的 deflate 流不保证与 zlib 逐字节相同（解压后一致）；gzip 头已对齐。
- 响应头**顺序**由 Go/nginx 决定（Go 的 `http.Header` 是 map），逐字一致的判定按「同名头取值」比较。
- `db_has_column` 的结果在 Go 里是**进程级**缓存（PHP 是每请求），跑完 `video` 列迁移需重启 goapi。
- Redis 在线集合在进程被 SIGKILL 时来不及 `SREM`，可能残留「僵尸成员」；判定在线请用
  `IsOnline`（看带 TTL 的单用户键），集合自身有 TTL 会兜底。
- WebSocket 的 4002（空闲关闭）关闭码契约未规定；`error` 帧的 key 顺序按契约写成 `type/code/msg`。
- 阶段 0 的事件来源仍是 1 秒 DB 轮询（与 PHP 同构），Redis Pub/Sub 只提供封装；
  换 Pub/Sub 的替换点是 `internal/realtime/hub.go` 的 `pollOnce`。
