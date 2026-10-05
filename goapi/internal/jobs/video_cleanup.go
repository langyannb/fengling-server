// Package jobs 承载 goapi 的**进程内常驻任务**。
//
// 阶段 1 只有一件事：视频清理 ticker —— 把 PHP 里「只能挂在上传成功之后」的
// 清理升级成常驻任务，语义与 api.php:4058 video_cleanup(true, true, true) 完全一致。
package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// ObjectStore 是清理需要的对象存储能力（抽象出来便于用假 store 单测）。
// *upload.Client 天然满足它。
type ObjectStore interface {
	// KeyFromURL 对齐 video_key_from_url：只认本站公共读前缀，否则返回空串。
	KeyFromURL(url string) string
	Delete(ctx context.Context, key string) error
}

// VideoStore 是清理需要的数据能力（*store.Store 满足它）。
type VideoStore interface {
	VideoConfig(ctx context.Context) store.VideoConfig
	QueryVideoRows(ctx context.Context) ([]store.VideoRow, error)
	MarkVideoCleaned(ctx context.Context, table string, id int64) error
}

// Result 对齐 PHP video_cleanup 的返回数组（+ skipped 原因，供日志观察）。
type Result struct {
	Deleted    int
	FreedBytes int64
	Visited    int
	Skipped    string
}

// PlanCleanup 复刻 api.php:4074-4089 的**选择决策**（纯函数，无 IO，可单测）：
//
//   - rows 必须已按「表顺序 + created_at ASC, id ASC」排好；
//   - respectAutoClean 且 auto_clean!=1 → 只统计不删（返回 Skipped 原因）；
//   - byDays 且 keep_days>0 → 删 created_at 早于 (now - keep_days 天) 的；
//   - byCapacity → 从最旧开始删，直到 total <= total_limit_mb。
//
// 返回的 []int 是 rows 的下标（按遍历顺序，与 PHP 的 $doomed 顺序一致）。
func PlanCleanup(rows []store.VideoRow, cfg store.VideoConfig, byCapacity, byDays, respectAutoClean bool, now time.Time) ([]int, Result) {
	res := Result{Visited: len(rows)}
	if len(rows) == 0 {
		return nil, res
	}
	if respectAutoClean && cfg.AutoClean != 1 {
		res.Skipped = "auto_clean=0 只统计不删除"
		return nil, res
	}

	total := int64(0)
	for _, r := range rows {
		total += r.Size
	}
	limitBytes := int64(cfg.TotalLimitMB) * 1024 * 1024
	var deadline int64
	if byDays && cfg.KeepDays > 0 {
		deadline = now.Unix() - int64(cfg.KeepDays)*86400
	}

	doomed := make([]int, 0, 8)
	for i, r := range rows {
		ts := parseCreatedAt(r.CreatedAt)
		// PHP: if ($deadline > 0 && $ts > 0 && $ts < $deadline)
		if deadline > 0 && ts > 0 && ts < deadline {
			doomed = append(doomed, i)
			total -= r.Size
			continue
		}
		// PHP: if ($byCapacity && $total > $limitBytes)
		if byCapacity && total > limitBytes {
			doomed = append(doomed, i)
			total -= r.Size
		}
	}
	return doomed, res
}

// parseCreatedAt 复刻 strtotime(created_at)：按 PHP 的默认时区（Asia/Shanghai）
// 解释 MySQL DATETIME 文本；解析不了返回 0（PHP 的 strtotime 失败也是 0）。
func parseCreatedAt(s string) int64 {
	if len(s) < len("2006-01-02 15:04:05") {
		return 0
	}
	t, err := time.ParseInLocation(store.DateTimeLayout, s[:len("2006-01-02 15:04:05")], config.LocalZone())
	if err != nil {
		return 0
	}
	return t.Unix()
}

// VideoCleaner 是常驻清理任务。
type VideoCleaner struct {
	store    VideoStore
	object   ObjectStore
	redis    *store.Redis
	log      *slog.Logger
	interval time.Duration
	initial  time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped bool
	done    chan struct{}
}

// CleanerDeps 是构造参数。
type CleanerDeps struct {
	Store  VideoStore
	Object ObjectStore
	Redis  *store.Redis
	Cfg    config.Config
	Log    *slog.Logger
}

// NewVideoCleaner 组装（不启协程；由调用方 `go cleaner.Run(ctx)`）。
func NewVideoCleaner(deps CleanerDeps) *VideoCleaner {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	interval := deps.Cfg.VideoCleanupInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	initial := deps.Cfg.VideoCleanupInitial
	if initial <= 0 {
		initial = 30 * time.Second
	}
	return &VideoCleaner{
		store:    deps.Store,
		object:   deps.Object,
		redis:    deps.Redis,
		log:      log,
		interval: interval,
		initial:  initial,
		done:     make(chan struct{}),
	}
}

// Run 常驻：启动 initial 后跑第一次，之后每 interval 一次，直到 ctx 取消或 Stop。
// 单次执行 panic 只记日志，绝不拖垮进程或终止 ticker。
func (c *VideoCleaner) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		cancel()
		close(c.done)
		return
	}
	c.cancel = cancel
	c.mu.Unlock()
	defer close(c.done)
	defer cancel()

	c.log.Info("视频清理任务已启动", "initial", c.initial.String(), "interval", c.interval.String())

	timer := time.NewTimer(c.initial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			c.log.Info("视频清理任务已停止")
			return
		case <-timer.C:
			c.safeRunOnce(ctx)
			timer.Reset(c.interval)
		}
	}
}

// Stop 取消并等待 Run 退出（幂等，可重复调用；最多等 stopWait）。
func (c *VideoCleaner) Stop() {
	c.mu.Lock()
	c.stopped = true
	cancel := c.cancel
	c.mu.Unlock()
	if cancel == nil {
		// Run 还没起来：把 done 关掉由 Run 自己负责，这里不阻塞。
		return
	}
	cancel()
	select {
	case <-c.done:
	case <-time.After(stopWait):
		c.log.Warn("等待视频清理任务退出超时, 继续关闭")
	}
}

// stopWait 是 Close 时等待后台任务退出的上限。
const stopWait = 3 * time.Second

// safeRunOnce 是带 panic 兜底的单次执行。
func (c *VideoCleaner) safeRunOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("视频清理任务 panic, 已忽略本次", "panic", r)
		}
	}()
	res := c.Cleanup(ctx)
	// 契约：只有 deleted>0 打 info，其余 debug（10 分钟一次，别刷日志）。
	if res.Deleted > 0 {
		c.log.Info("视频清理完成",
			"deleted", res.Deleted, "freed_bytes", res.FreedBytes,
			"visited", res.Visited, "skipped", res.Skipped)
		return
	}
	c.log.Debug("视频清理完成",
		"deleted", res.Deleted, "freed_bytes", res.FreedBytes,
		"visited", res.Visited, "skipped", res.Skipped)
}

// Cleanup 等于 PHP 的 video_cleanup(true, true, true)（上传成功后也会调这个）。
// MySQL 不可用时只记日志并返回零值结果，绝不 panic、绝不影响上传响应。
func (c *VideoCleaner) Cleanup(ctx context.Context) Result {
	cfg := c.store.VideoConfig(ctx)
	rows, err := c.store.QueryVideoRows(ctx)
	if err != nil && len(rows) == 0 {
		c.log.Warn("视频清理: 读取待清理行失败", "err", err)
		return Result{}
	}
	doomed, res := PlanCleanup(rows, cfg, true, true, true, time.Now())
	for _, i := range doomed {
		c.cleanupOne(ctx, rows[i], &res)
	}
	if c.redis != nil {
		c.redis.SetLastCleanup(ctx, time.Now(), 24*time.Hour)
	}
	return res
}

// cleanupOne 对齐 api.php:4118 video_cleanup_one：先删对象（失败忽略），
// 再改库（失败不算 deleted）。video_size 保留做审计。
func (c *VideoCleaner) cleanupOne(ctx context.Context, row store.VideoRow, out *Result) {
	if c.object != nil {
		if key := c.object.KeyFromURL(row.Video); key != "" {
			dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if err := c.object.Delete(dctx, key); err != nil {
				c.log.Warn("清理视频对象失败(继续清理消息)", "key", key, "err", err)
			}
			cancel()
		}
	}
	if err := c.store.MarkVideoCleaned(ctx, row.Table, row.ID); err != nil {
		c.log.Warn("清理视频消息失败", "table", row.Table, "id", row.ID, "err", err)
		return
	}
	out.Deleted++
	out.FreedBytes += row.Size
}
