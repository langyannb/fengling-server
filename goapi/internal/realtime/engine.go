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

// New 组装 Engine（不建任何连接）。
func New(cfg config.Config, db *store.Store, rd *store.Redis, log *slog.Logger) *Engine {
	return &Engine{
		cfg:     cfg,
		db:      db,
		rd:      rd,
		log:     log,
		hub:     newHub(cfg, db, rd, log),
		started: time.Now(),
	}
}

// Close 让所有在途连接立刻收尾（进程退出用，可重复调用）。
func (e *Engine) Close() {
	e.closed.Do(func() { e.hub.closeAll() })
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
