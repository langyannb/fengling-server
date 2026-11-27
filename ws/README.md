# fengling-ws

风铃分享库的 WebSocket 消息服务（B 方案）。替换 goapi 里没有源码的 `/ws`，负责：

- 连接鉴权（`sessions` / `users.token`）
- `hello` 游标、15 秒业务心跳、ping/pong
- `send` → 转发 PHP `pm_send` / `social_send`，返回 `ack`
- 500ms 轮询 MySQL，把新消息 fanout 给在线用户（兼容旧客户端/PHP 发送）
- `read` 已读回执（写 `social_pm_reads` / `social_reads`）
- `typing` 输入中（只走内存，不落库）

## 运行

```bash
export FENGLING_WS_ADDR=127.0.0.1:9101
export FENGLING_DB_DSN='user:pass@tcp(127.0.0.1:3306)/flfxk?parseTime=true&charset=utf8mb4'
export FENGLING_API_BASE='http://127.0.0.1:9845/api.php'
go run .
```

健康检查：`GET /healthz`。

## 协议

客户端连接：`ws://host/ws?pm_id=0&group_id=0`，`Authorization: Bearer <token>`。

```json
{"type":"hello","data":{"user_id":1,"server_time":0,"heartbeat_sec":15,"cursor":{"pm_id":0,"group_id":0}}}
{"type":"send","data":{"client_msg_id":"uuid","to_user":2,"content":"hi"}}
{"type":"send","data":{"client_msg_id":"uuid","group_id":3,"content":"hi","at":[2],"quote_id":0}}
{"type":"ack","data":{"client_msg_id":"uuid","id":10,"conv_id":1,"created_at":"..."}}
{"type":"read","data":{"conv_id":1,"last_read_id":10}}
{"type":"typing","data":{"conv_id":1}}
{"type":"ping","data":{}}
{"type":"error","code":401,"msg":"登录已失效"}
```

推送帧 `pm` / `group` 的字段与旧 SSE 完全一致，客户端解析不用改。

## 部署

```bash
go build -trimpath -ldflags "-s -w" -o fengling-ws .
install -m 755 fengling-ws /opt/fengling/fengling-ws
install -m 644 deploy/fengling-ws.service /etc/systemd/system/
install -m 600 deploy/ws.env.example /etc/fengling/ws.env   # 填 DSN
systemctl daemon-reload && systemctl enable --now fengling-ws
curl -fsS http://127.0.0.1:9101/healthz
```

nginx 把 `/ws` 从 9100 改到 9101，配置见 `deploy/nginx-ws.conf`；切换前保留旧 SSE，客户端全部升级后再下线。
