package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

const (
	// wsCloseUnauthorized 是契约规定的「未登录」关闭码。
	wsCloseUnauthorized = 4001
	// wsCloseIdle 是 60 秒没有任何入站帧（无 pong）时的关闭码；契约没规定，这里取 4002。
	wsCloseIdle = 4002
	// wsOnlineRenewEvery 是 Redis 在线标记的续期间隔（在线键 TTL 90s）。
	wsOnlineRenewEvery = 10 * time.Second
)

// wsUpgrader：鉴权靠 token，跨域由 nginx 控制，所以这里放开 Origin 检查。
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// ServeWS 实现契约第 5 节的 WebSocket 协议。
//
// 与 SSE 的关键差异：**不做 25 秒封顶**（这是引入 WS 的收益之一），
// 心跳 15s、60s 无任何入站帧才关、写超时 10s；
// 事件投递走 conn.send（缓冲 64，满则丢弃并计数） -> 单一写 goroutine。
func (e *Engine) ServeWS(w http.ResponseWriter, r *http.Request, me *store.User, pmID, grpID int64) {
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		e.log.Warn("WebSocket 升级失败", "err", err)
		return
	}
	defer ws.Close()

	if me == nil {
		// 契约：未登录也要先完成升级，把原因作为 error 帧发出去，再以 4001 关闭。
		_ = ws.SetWriteDeadline(time.Now().Add(e.cfg.WSWriteWait))
		_ = ws.WriteMessage(websocket.TextMessage, phpjson.Marshal(phpjson.New().
			Set("type", "error").
			Set("code", 401).
			Set("msg", "登录已失效")))
		_ = ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(wsCloseUnauthorized, "登录已失效"),
			time.Now().Add(2*time.Second))
		return
	}

	ctx := r.Context()
	pmCur := e.resolveCursor(ctx, "social_pm_messages", pmID)
	grpCur := e.resolveCursor(ctx, "social_messages", grpID)

	c := e.hub.add(me.ID, "ws", pmCur, grpCur)
	defer e.hub.remove(c)

	e.rd.MarkOnline(ctx, me.ID, e.cfg.OnlineTTL)
	defer e.rd.MarkOffline(context.WithoutCancel(ctx), me.ID)

	// hello 必须在写 goroutine 之前发（保证帧序：hello 一定是最先出去的那一帧）
	if !e.wsWrite(ws, phpjson.Marshal(phpjson.New().
		Set("type", "hello").
		Set("data", phpjson.New().
			Set("user_id", me.ID).
			Set("server_time", time.Now().Unix()).
			Set("heartbeat_sec", int64(e.cfg.WSHeartbeat/time.Second)).
			Set("cursor", phpjson.New().
				Set("pm_id", c.pmCur.Load()).
				Set("group_id", c.grpCur.Load()))))) {
		return
	}

	go e.wsWritePump(ctx, ws, c)
	e.wsReadPump(ctx, ws, c)
}

// wsWritePump 是唯一的写 goroutine：ping、pong、事件帧全从这里出去。
func (e *Engine) wsWritePump(ctx context.Context, ws *websocket.Conn, c *conn) {
	defer ws.Close() // 写侧一结束就关底层连接，让读侧立刻返回

	ping := time.NewTicker(e.cfg.WSHeartbeat)
	defer ping.Stop()
	renew := time.NewTicker(wsOnlineRenewEvery)
	defer renew.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			// 进程关闭：礼貌地发一个 close 帧
			_ = ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, ""),
				time.Now().Add(2*time.Second))
			return
		case frame := <-c.send:
			if !e.wsWrite(ws, frame) {
				return
			}
		case <-ping.C:
			if !e.wsWrite(ws, phpjson.Marshal(phpjson.New().
				Set("type", "ping").
				Set("data", phpjson.New().Set("t", time.Now().Unix())))) {
				return
			}
		case <-renew.C:
			e.rd.RefreshOnline(ctx, c.userID, e.cfg.OnlineTTL)
		}
	}
}

// wsReadPump 处理客户端 → 服务端的三种帧：ping / pong / cursor。
func (e *Engine) wsReadPump(ctx context.Context, ws *websocket.Conn, c *conn) {
	defer ws.Close()

	ws.SetReadLimit(1 << 20)
	_ = ws.SetReadDeadline(time.Now().Add(e.cfg.WSIdle))

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			// 读超时 = 60 秒没有任何入站帧（等价「60s 无 pong」）；其余是客户端断开。
			return
		}
		// 任何入站帧都算「这条连接活着」，续上空闲计时
		_ = ws.SetReadDeadline(time.Now().Add(e.cfg.WSIdle))

		var msg struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "ping":
			e.hub.dispatch(c, phpjson.Marshal(phpjson.New().
				Set("type", "pong").
				Set("data", phpjson.New().Set("t", time.Now().Unix()))))
		case "pong":
			// 只是存活信号，deadline 上面已经刷新
		case "cursor":
			var d struct {
				PMID    int64 `json:"pm_id"`
				GroupID int64 `json:"group_id"`
			}
			if len(msg.Data) == 0 || json.Unmarshal(msg.Data, &d) != nil {
				continue
			}
			// 语义同 SSE：<=0 表示「从现在开始」，取当前 MAX(id)（绝不重放历史）。
			if d.PMID <= 0 {
				d.PMID = e.resolveCursor(ctx, "social_pm_messages", 0)
			}
			if d.GroupID <= 0 {
				d.GroupID = e.resolveCursor(ctx, "social_messages", 0)
			}
			c.pmCur.Store(d.PMID)
			c.grpCur.Store(d.GroupID)
		}
	}
}

// wsWrite 带 10 秒写超时的单帧发送（只有写 goroutine 会调用）。
func (e *Engine) wsWrite(ws *websocket.Conn, frame []byte) bool {
	_ = ws.SetWriteDeadline(time.Now().Add(e.cfg.WSWriteWait))
	return ws.WriteMessage(websocket.TextMessage, frame) == nil
}
