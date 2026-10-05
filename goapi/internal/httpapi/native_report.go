package httpapi

import "net/http"

// 阶段 4 匿名上报 action 的原生实现，逐字对齐 api.php:3034-3080。
//
// 这两个 action 是**匿名**的：不读 token、不查 users，直接写库。
// 文案与业务码：crash_report 的「stack 为空」是 code 400，harm_report 的两条限流是 code 429；
// 经 statusForCode 映射后**都是 HTTP 400**（只有 401/404/500 才改 HTTP 状态码）。

// ---------- crash_report (api.php:3034) ----------

func (rt *Router) handleCrashReport(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "crash_report")
	stack := c.ParamStr("stack", "")
	if mbStrlen(stack) > 20000 {
		stack = mbSubstr(stack, 0, 20000)
	}
	if stack == "" {
		c.Error("stack 为空", 400)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO crash_reports (device, android_version, app_version, stack, ip) VALUES (?, ?, ?, ?, ?)",
		mbSubstr(c.ParamStr("device", ""), 0, 100),
		mbSubstr(c.ParamStr("android_version", ""), 0, 30),
		mbSubstr(c.ParamStr("app_version", ""), 0, 30),
		stack,
		clientIP(c.R)); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- harm_report (api.php:3055) ----------

func (rt *Router) handleHarmReport(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "harm_report")
	content := c.ParamStr("content", "")
	if mbStrlen(content) > 500 {
		content = mbSubstr(content, 0, 500)
	}
	if phpTrim(content) == "" {
		c.Error("请填写哪里被和谐了", 400)
		return
	}
	contact := c.ParamStr("contact", "")
	if mbStrlen(contact) > 100 {
		contact = mbSubstr(contact, 0, 100)
	}
	appID := c.ParamInt("app_id", 0)
	ip := clientIP(c.R)

	// ① 同 IP 60 秒内最多 1 条
	v, hasRow, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM harm_reports WHERE ip = ? AND created_at > (NOW() - INTERVAL 60 SECOND)", ip)
	if err != nil {
		c.dbError(err)
		return
	}
	if intValue(v, hasRow) > 0 {
		c.Error("提交太频繁，请稍后再试", 429)
		return
	}
	// ② 同 IP 同一软件 10 分钟内最多 1 条
	v, hasRow, err = rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM harm_reports WHERE ip = ? AND app_id = ? AND created_at > (NOW() - INTERVAL 600 SECOND)", ip, appID)
	if err != nil {
		c.dbError(err)
		return
	}
	if intValue(v, hasRow) > 0 {
		c.Error("该软件刚刚反馈过，请勿重复提交", 429)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO harm_reports (app_id, app_name, content, contact, ip) VALUES (?, ?, ?, ?, ?)",
		appID,
		mbSubstr(c.ParamStr("app_name", ""), 0, 100),
		content,
		contact,
		ip); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}
