package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// Engine 是实时通道的入口：SSE（逐字对齐 PHP）与 WebSocket（新增能力）。
type Engine struct {
	cfg config.Config
	db  *store.Store
	rd  *store.Redis
	log *slog.Logger
	hub *Hub

	started time.Time
	closed  sync.Once
}

// closeWait 是 Engine.Close 等待「所有连接收尾完成」的上限（含 Redis MarkOffline）。
// 超时只记日志继续，绝不挂死进程。
const closeWait = 3 * time.Second

// New 组装 Engine（不建任何连接），并启动 WS 的全局轮询泵（幂等）。
//
// 泵在进程启动时就起来，但**只在有 WS 连接时才查库**（见 pumpLoop）；
// 没有它 WS 连接永远收不到任何事件（SSE 连接靠自己的循环取数）。
func New(cfg config.Config, db *store.Store, rd *store.Redis, log *slog.Logger) *Engine {
	e := &Engine{
		cfg:     cfg,
		db:      db,
		rd:      rd,
		log:     log,
		hub:     newHub(cfg, db, rd, log),
		started: time.Now(),
	}
	e.hub.startPump()
	return e
}

// Close 让所有在途连接立刻收尾，并**等它们跑完收尾动作**再返回（进程退出用，可重复调用）。
//
// 顺序很重要：先停轮询泵（不再产生投递），再给所有长连接发退出信号，
// 最后等 connWG 归零 —— 各连接的 defer 链里 rd.MarkOffline 早于 hub.remove 注册，
// 所以「等归零」就等于「等 MarkOffline 跑完」，main 里随后的 defer rd.Close() 才不会
// 打出 `redis: client is closed`（重启后在线键也不用再靠 90 秒 TTL 自然过期）。
func (e *Engine) Close() {
	e.closed.Do(func() {
		e.hub.stopPump()
		e.hub.closeAll()
		if !e.hub.waitConns(closeWait) {
			e.log.Warn("实时连接收尾超时, 继续关闭", "active", e.hub.count())
		}
	})
}

// ActiveConns 当前实时连接数（运维观测用）。
func (e *Engine) ActiveConns() int { return e.hub.count() }

// resolveCursor 对齐 sse_run 开头的游标语义：<=0 就取「当前最大 id」，绝不重放历史。
func (e *Engine) resolveCursor(ctx context.Context, table string, cur int64) int64 {
	if cur > 0 {
		return cur
	}
	max, err := e.db.MaxID(ctx, table)
	if err != nil {
		e.log.Warn("游标初始化失败, 退化为 0", "table", table, "err", err)
		return 0
	}
	return max
}
