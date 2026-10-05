# goapi —— 风铃分享库 Go 网关（阶段 0）

监听 `127.0.0.1:9100`，**原生**接管 4 个入口，其余全部 action **原样透传**给现有 PHP：

| 入口 | 处理方式 |
| --- | --- |
| `?action=health` | Go 原生（新增能力，PHP 没有） |
| `?action=version` | Go 原生（与 PHP 逐字节一致） |
| `?action=stream` | Go 原生 SSE（逐字节对齐 `api.php:3648 sse_run()`） |
| `POST /ws`（`location = /ws`） | Go 原生 WebSocket（新增能力） |
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
internal/realtime/hub.go    连接注册表 + 1 秒轮询（阶段 3/4 换成 Redis Pub/Sub 的替换点）
internal/realtime/sse.go    原生 SSE
internal/realtime/ws.go     原生 WebSocket
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
