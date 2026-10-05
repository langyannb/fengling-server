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
