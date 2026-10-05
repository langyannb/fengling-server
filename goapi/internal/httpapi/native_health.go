package httpapi

import (
	"net/http"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// handleHealth 是**新增**能力（api.php 里没有健康检查）。
//
// 响应壳仍走 json_out 语义（code/msg/data + Cache-Control: no-store + gzip 规则），
// data 字段：ok / build / time / uptime_sec / redis / mysql。
//
// ok 表示「网关进程本身活着并且能把请求处理完」；依赖状态分别由 redis/mysql 字段
// 给出，方便运维一眼区分「网关挂了」和「Redis 挂了但网关还能降级服务」。
func (rt *Router) handleHealth(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "health")
	ctx := r.Context()

	mysql := "down"
	if err := rt.env.Store.Ping(ctx); err == nil {
		mysql = "up"
	}

	redis := "down"
	switch {
	case !rt.env.Redis.Enabled():
		// 没配 Redis 不算故障，只是没启用；显式标 down 让运维看得见。
		redis = "down"
	case rt.env.Redis.Ping(ctx) == nil:
		redis = "up"
	}

	data := phpjson.New().
		Set("ok", true).
		Set("build", rt.env.Version).
		Set("time", rt.env.BuildTime).
		Set("uptime_sec", int64(time.Since(rt.env.Started).Seconds())).
		Set("redis", redis).
		Set("mysql", mysql)

	c.JSON(data, 0, "ok", 0)
}

// handleHealthz 是阶段 1 新增的最小运维端点。
//
// 契约第 6 节要求把「最近一次视频清理时间」写进 Redis（goapi:video:last_cleanup，
// TTL 1 天）供 /healthz 展示；仓库里原本没有 /healthz，这里补上。
// nginx 只反代 /api.php 与 /ws，所以它仅在 127.0.0.1:9100 上可达，不影响线上对比。
func (rt *Router) handleHealthz(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "healthz")
	last := int64(0)
	if at, ok := rt.env.Redis.LastCleanup(r.Context()); ok {
		last = at
	}
	c.JSON(phpjson.New().
		Set("ok", true).
		Set("video_last_cleanup", last), 0, "ok", 0)
}
