package realtime

import (
	"context"
	"net/http"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// ServeSSE 是 `?action=stream` 的原生实现，逐字对齐 api.php:3648 sse_run()。
//
// 客户端（ApiClient.streamMessages）是权威消费者：它按行解析 `event:` / `data:`，
// `:` 开头是心跳注释，`event: bye` 视为服务端正常收尾并立刻重连 —— 所以这里的
// 字节格式、时序、封顶时间都不能改。
//
// 与 PHP 的**关键差异（收益）**：PHP 只在每轮循环开头查 connection_aborted()，
// 而 ignore_user_abort(true) 下它通常要等到写失败才发现客户端走了，于是白占
// worker 最多 25 秒。这里用 r.Context()：客户端一断，select 立刻命中 <-ctx.Done()，
// 循环立即返回、DB 轮询立刻停止。
func (e *Engine) ServeSSE(w http.ResponseWriter, r *http.Request, me *store.User, pmID, grpID int64) {
	ctx := r.Context()

	// ① 先解析游标（PHP 也是在发头之前做的）
	pmCur := e.resolveCursor(ctx, "social_pm_messages", pmID)
	grpCur := e.resolveCursor(ctx, "social_messages", grpID)

	// ② 长连接响应头（顺序由 Go 统一排序；nginx 侧还会吃掉 X-Accel-Buffering）
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	fl, _ := w.(http.Flusher)
	w.WriteHeader(http.StatusOK)

	// ③ 先发 retry（对齐 api.php:3675），注意**不**立刻发心跳：首个 `: hb` 在第 10 秒
	if !writeRaw(w, fl, "retry: 2000\n\n") {
		return
	}

	c := e.hub.add(me.ID, "sse", pmCur, grpCur)
	defer e.hub.remove(c)

	e.rd.MarkOnline(ctx, me.ID, e.cfg.OnlineTTL)
	defer e.rd.MarkOffline(context.WithoutCancel(ctx), me.ID)

	start := time.Now()
	lastHB := start
	muted := e.db.MutedGroupIDs(ctx, me.ID)

	// 第一轮立即执行（PHP 发完头就查库），之后每 1 秒一轮，末尾 sleep —— 与 usleep(1000000) 同构
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done(): // 客户端断开：立刻退出，不等 25 秒
			return
		case <-c.closed: // 进程关闭
			return
		case <-timer.C:
		}

		now := time.Now()
		if now.Sub(start) >= e.cfg.SSEMaxAge { // 25 秒封顶
			writeRaw(w, fl, byeFrame)
			return
		}
		if now.Sub(lastHB) >= e.cfg.SSEHeartbeat { // 10 秒心跳
			if !writeRaw(w, fl, ": hb\n\n") {
				return
			}
			muted = e.db.MutedGroupIDs(ctx, me.ID) // 免打扰变化 10 秒内生效
			e.rd.RefreshOnline(ctx, me.ID, e.cfg.OnlineTTL)
			lastHB = now
		}

		ok := true
		e.hub.pollOnce(ctx, c, muted, func(ev event) bool {
			if !writeEvent(w, fl, ev) {
				ok = false
				return false
			}
			return true
		})
		if !ok || ctx.Err() != nil {
			return
		}

		timer.Reset(e.cfg.PollInterval)
	}
}

// byeFrame 是 25 秒封顶时的收尾帧（api.php:3688 `sse_event('bye', ['reason' => 'timeout'])`）。
const byeFrame = "event: bye\ndata: {\"reason\":\"timeout\"}\n\n"

// writeEvent 输出一条标准 SSE 事件：`event: <名>\ndata: <json>\n\n`（对齐 api.php:3779 sse_event）。
func writeEvent(w http.ResponseWriter, fl http.Flusher, ev event) bool {
	buf := make([]byte, 0, 256)
	buf = append(buf, "event: "...)
	buf = append(buf, ev.name...)
	buf = append(buf, '\n')
	buf = append(buf, "data: "...)
	buf = append(buf, phpjson.Marshal(ev.data)...)
	buf = append(buf, '\n', '\n')
	return writeRaw(w, fl, string(buf))
}

// writeRaw 写一段原始字节并立即 flush（每条事件都必须马上出去）。
func writeRaw(w http.ResponseWriter, fl http.Flusher, s string) bool {
	if _, err := w.Write([]byte(s)); err != nil {
		return false
	}
	if fl != nil {
		fl.Flush()
	}
	return true
}
