package httpapi

import (
	"net/http"
)

// Router 是 goapi 的入口 Handler：原生 action 自己处理，其余全部透传 PHP。
type Router struct {
	env  Env
	fcgi *FCGI
}

// New 组装 Router（main.go 调用一次）。
func New(env Env) *Router {
	return &Router{env: env, fcgi: NewFCGI(env.Cfg, env.Log)}
}

// ServeHTTP 分发。
//
// 分发规则（对齐 api.php）：
//   - OPTIONS 一律 204 + CORS（api.php:31-34 在 switch 之前无条件返回）；
//   - /ws 走 WebSocket（nginx `location = /ws` 单独反代）；
//   - action 只从查询串取（PHP 用 $_GET['action']）；
//   - 未知 action **不自己回 404**，透传给 PHP 让它回 `404 未知操作: xxx`。
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORS(w.Header())
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.URL.Path == "/ws" {
		rt.handleWS(w, r)
		return
	}

	switch r.URL.Query().Get("action") {
	case "health":
		setCORS(w.Header())
		rt.handleHealth(w, r)
	case "version":
		setCORS(w.Header())
		rt.handleVersion(w, r)
	case "stream":
		setCORS(w.Header())
		rt.handleStream(w, r)
	default:
		// 透传路径**不预设任何头**：CORS 由 PHP 自己发，避免重复。
		rt.fcgi.Serve(w, r)
	}
}

// handleStream 对齐 api.php:1182-1187：先鉴权，未登录返回普通 JSON 401（不进流模式）。
func (rt *Router) handleStream(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "stream")
	me, err := rt.env.Store.CurrentUser(r.Context(), c.Token())
	if err != nil {
		// 同 version：文案里不带任何 Go 文件名/行号，细节只进日志。
		c.Log().Error("stream 鉴权查询失败", "err", err)
		c.Error("服务器错误: "+err.Error(), 500)
		return
	}
	if me == nil {
		c.Error("登录已失效", 401)
		return
	}
	// 游标由 httpapi 用 PHP 的 (int) 语义解析后传给 realtime（<=0 = 从现在开始）。
	rt.env.Engine.ServeSSE(w, r, me, c.ParamInt("pm_id", 0), c.ParamInt("group_id", 0))
}

// handleWS 对齐契约第 5 节：token 可来自 ?token= 或 Authorization: Bearer；
// 未登录也要先完成升级、发 error 帧、再以 4001 关闭（客户端才能读到原因）。
func (rt *Router) handleWS(w http.ResponseWriter, r *http.Request) {
	user, err := rt.env.Store.CurrentUser(r.Context(), QueryToken(r))
	if err != nil {
		rt.env.Log.Warn("WebSocket 鉴权查询失败, 按未登录处理", "err", err)
	}
	pmID := int64(0)
	grpID := int64(0)
	if v := r.URL.Query().Get("pm_id"); v != "" {
		pmID = atoiPrefix(v)
	}
	if v := r.URL.Query().Get("group_id"); v != "" {
		grpID = atoiPrefix(v)
	}
	rt.env.Engine.ServeWS(w, r, user, pmID, grpID)
}
