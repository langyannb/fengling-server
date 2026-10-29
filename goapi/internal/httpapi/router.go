package httpapi

import (
	"net/http"

	"github.com/langyannb/fengling-server/goapi/internal/mailer"
)

// Router 是 goapi 的入口 Handler：原生 action 自己处理，其余全部透传 PHP。
type Router struct {
	env  Env
	fcgi *FCGI
}

// New 组装 Router（main.go 调用一次）。
//
// 阶段 2 起把两份可选依赖补上默认实现：账号 action 的数据访问（*store.Store）
// 与验证码邮件发送（mailer.Client）。单测可以直接在 Env 里注入假实现。
func New(env Env) *Router {
	if env.Accounts == nil && env.Store != nil {
		env.Accounts = env.Store
	}
	// 注意不能写成 env.Cols = env.Store：*store.Store 为 nil 时接口会变成「非 nil 的
	// 接口包着 nil 指针」，HasColumn 会被调到 nil 接收者上。
	if env.Cols == nil && env.Store != nil {
		env.Cols = env.Store
	}
	if env.VideoCfg == nil && env.Store != nil {
		env.VideoCfg = env.Store
	}
	if env.Mailer == nil {
		env.Mailer = mailer.New(mailer.Config{
			Host:     env.Cfg.SMTPHost,
			Port:     env.Cfg.SMTPPort,
			User:     env.Cfg.SMTPUser,
			Pass:     env.Cfg.SMTPPass,
			FromName: env.Cfg.SMTPFromName,
		}, env.Log)
	}
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
		// 补一处阶段 0 的对齐差异：PHP 在 http_response_code(204) 时仍会带上默认的
		// Content-Type: text/html; charset=UTF-8，Go 的 204 默认不带。
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.URL.Path == "/ws" {
		rt.handleWS(w, r)
		return
	}

	// 阶段 1 新增的最小运维端点（契约第 6 节要求展示最近一次视频清理时间）。
	// nginx 只反代 /api.php 与 /ws，所以它只在 127.0.0.1:9100 上可达。
	if r.URL.Path == "/healthz" {
		setCORS(w.Header())
		rt.handleHealthz(w, r)
		return
	}

	switch r.URL.Query().Get("action") {
	case "health":
		setCORS(w.Header())
		rt.handleHealth(w, r)

	// ---------- 阶段 5B-1：后台社群/消息/用户管理（22 个，见 native_admin_social.go） ----------
	case "admin_groups":
		setCORS(w.Header())
		rt.handleAdminGroups(w, r)
	case "admin_group_save":
		setCORS(w.Header())
		rt.handleAdminGroupSave(w, r)
	case "admin_group_delete":
		setCORS(w.Header())
		rt.handleAdminGroupDelete(w, r)
	case "admin_group_members":
		setCORS(w.Header())
		rt.handleAdminGroupMembers(w, r)
	case "admin_group_member_remove":
		setCORS(w.Header())
		rt.handleAdminGroupMemberRemove(w, r)
	case "admin_social_messages":
		setCORS(w.Header())
		rt.handleAdminSocialMessages(w, r)
	case "admin_social_message_delete":
		setCORS(w.Header())
		rt.handleAdminSocialMessageDelete(w, r)
	case "admin_social_message_clear":
		setCORS(w.Header())
		rt.handleAdminSocialMessageClear(w, r)
	case "admin_pm_conversations":
		setCORS(w.Header())
		rt.handleAdminPmConversations(w, r)
	case "admin_pm_messages":
		setCORS(w.Header())
		rt.handleAdminPmMessages(w, r)
	case "admin_pm_message_delete":
		setCORS(w.Header())
		rt.handleAdminPmMessageDelete(w, r)
	case "admin_pm_clear":
		setCORS(w.Header())
		rt.handleAdminPmClear(w, r)
	case "admin_notification_list":
		setCORS(w.Header())
		rt.handleAdminNotificationList(w, r)
	case "admin_notification_delete":
		setCORS(w.Header())
		rt.handleAdminNotificationDelete(w, r)
	case "admin_notify_send":
		setCORS(w.Header())
		rt.handleAdminNotifySend(w, r)
	case "admin_users":
		setCORS(w.Header())
		rt.handleAdminUsers(w, r)
	case "admin_user_save":
		setCORS(w.Header())
		rt.handleAdminUserSave(w, r)
	case "admin_user_delete":
		setCORS(w.Header())
		rt.handleAdminUserDelete(w, r)
	case "admin_user_tags_set":
		setCORS(w.Header())
		rt.handleAdminUserTagsSet(w, r)
	case "admin_tags":
		setCORS(w.Header())
		rt.handleAdminTags(w, r)
	case "admin_tag_preset_save":
		setCORS(w.Header())
		rt.handleAdminTagPresetSave(w, r)
	case "admin_tag_preset_delete":
		setCORS(w.Header())
		rt.handleAdminTagPresetDelete(w, r)

	case "version":
		setCORS(w.Header())
		rt.handleVersion(w, r)
	case "stream":
		setCORS(w.Header())
		rt.handleStream(w, r)
	case "user_avatar":
		setCORS(w.Header())
		rt.handleUserAvatar(w, r)
	case "social_image_upload":
		setCORS(w.Header())
		rt.handleSocialImageUpload(w, r)
	case "social_video_upload":
		setCORS(w.Header())
		rt.handleSocialVideoUpload(w, r)
	case "upload":
		setCORS(w.Header())
		rt.handleUpload(w, r)
	case "upload_apk":
		setCORS(w.Header())
		rt.handleUploadApk(w, r)
	case "video_config":
		setCORS(w.Header())
		rt.handleVideoConfig(w, r)

	// ---------- 阶段 2：账号与鉴权（11 个） ----------
	case "login":
		setCORS(w.Header())
		rt.handleLogin(w, r)
	case "send_code":
		setCORS(w.Header())
		rt.handleSendCode(w, r)
	case "register":
		setCORS(w.Header())
		rt.handleRegister(w, r)
	case "user_me":
		setCORS(w.Header())
		rt.handleUserMe(w, r)
	case "user_update":
		setCORS(w.Header())
		rt.handleUserUpdate(w, r)
	case "user_password":
		setCORS(w.Header())
		rt.handleUserPassword(w, r)
	case "captcha":
		setCORS(w.Header())
		rt.handleCaptcha(w, r)
	case "email_verify_send":
		setCORS(w.Header())
		rt.handleEmailVerifySend(w, r)
	case "email_verify":
		setCORS(w.Header())
		rt.handleEmailVerify(w, r)
	case "user_profile":
		setCORS(w.Header())
		rt.handleUserProfile(w, r)
	case "logout":
		setCORS(w.Header())
		rt.handleLogout(w, r)

	// ---------- 阶段 3：群聊（13 个 action + 2 个禁言管理） ----------
	case "social_groups":
		setCORS(w.Header())
		rt.handleSocialGroups(w, r)
	case "social_group":
		setCORS(w.Header())
		rt.handleSocialGroup(w, r)
	case "social_messages":
		setCORS(w.Header())
		rt.handleSocialMessages(w, r)
	case "social_read":
		setCORS(w.Header())
		rt.handleSocialRead(w, r)
	case "social_send":
		setCORS(w.Header())
		rt.handleSocialSend(w, r)
	case "social_recall":
		setCORS(w.Header())
		rt.handleSocialRecall(w, r)
	case "social_group_notice_set":
		setCORS(w.Header())
		rt.handleSocialGroupNoticeSet(w, r)
	case "social_group_allmute_set":
		setCORS(w.Header())
		rt.handleSocialGroupAllmuteSet(w, r)
	case "social_group_join":
		setCORS(w.Header())
		rt.handleSocialGroupJoin(w, r)
	case "social_group_leave":
		setCORS(w.Header())
		rt.handleSocialGroupLeave(w, r)
	case "social_group_images":
		setCORS(w.Header())
		rt.handleSocialGroupImages(w, r)
	case "social_mute_set":
		setCORS(w.Header())
		rt.handleSocialMuteSet(w, r)
	case "social_group_members":
		setCORS(w.Header())
		rt.handleSocialGroupMembers(w, r)
	case "admin_user_mute":
		setCORS(w.Header())
		rt.handleAdminUserMute(w, r)
	case "admin_user_unmute":
		setCORS(w.Header())
		rt.handleAdminUserUnmute(w, r)
	// 阶段 4：私聊 + 通知（其余 action 继续走 default 的 FastCGI 透传）
	case "pm_conversations":
		setCORS(w.Header())
		rt.handlePmConversations(w, r)
	case "pm_messages":
		setCORS(w.Header())
		rt.handlePmMessages(w, r)
	case "pm_send":
		setCORS(w.Header())
		rt.handlePmSend(w, r)
	case "pm_read":
		setCORS(w.Header())
		rt.handlePmRead(w, r)
	case "pm_recall":
		setCORS(w.Header())
		rt.handlePmRecall(w, r)
	case "notifications":
		setCORS(w.Header())
		rt.handleNotifications(w, r)
	case "notification_read":
		setCORS(w.Header())
		rt.handleNotificationRead(w, r)
	case "notification_delete":
		setCORS(w.Header())
		rt.handleNotificationDelete(w, r)
	case "crash_report":
		setCORS(w.Header())
		rt.handleCrashReport(w, r)
	case "harm_report":
		setCORS(w.Header())
		rt.handleHarmReport(w, r)

	// ---------- 阶段 5A：抽奖（原生 12 个） ----------
	// ⛔ 下面 6 个**刻意不注册**，落到 default 继续 FastCGI 透传 PHP：
	//   ① 破坏性 4 个：admin_lottery_activity_reset / admin_lottery_codes_delete /
	//      admin_lottery_prize_delete / admin_lottery_codes_import；
	//   ② 全表 UPDATE 2 个：admin_lottery_quota_reset_all / admin_lottery_quota_all
	//      —— 验收脚本的安全门禁（FORBIDDEN_ADMIN）永不调用它们，保留透传可避免
	//      「未经线上双跑验证就上线」；面板极少用，零风险。
	case "lottery_info":
		setCORS(w.Header())
		rt.handleLotteryInfo(w, r)
	case "lottery_draw":
		setCORS(w.Header())
		rt.handleLotteryDraw(w, r)
	case "lottery_records":
		setCORS(w.Header())
		rt.handleLotteryRecords(w, r)
	case "admin_lottery_prizes":
		setCORS(w.Header())
		rt.handleAdminLotteryPrizes(w, r)
	case "admin_lottery_prize_save":
		setCORS(w.Header())
		rt.handleAdminLotteryPrizeSave(w, r)
	case "admin_lottery_codes":
		setCORS(w.Header())
		rt.handleAdminLotteryCodes(w, r)
	case "admin_lottery_config_get":
		setCORS(w.Header())
		rt.handleAdminLotteryConfigGet(w, r)
	case "admin_lottery_config_set":
		setCORS(w.Header())
		rt.handleAdminLotteryConfigSet(w, r)
	case "admin_lottery_window_preview":
		setCORS(w.Header())
		rt.handleAdminLotteryWindowPreview(w, r)
	case "admin_lottery_draws":
		setCORS(w.Header())
		rt.handleAdminLotteryDraws(w, r)
	case "admin_lottery_quota_set":
		setCORS(w.Header())
		rt.handleAdminLotteryQuotaSet(w, r)
	case "admin_lottery_quota_reset":
		setCORS(w.Header())
		rt.handleAdminLotteryQuotaReset(w, r)
	// 阶段 5A 附带：about / notice 的**读取**（写入侧继续透传）
	case "about_config_get":
		setCORS(w.Header())
		rt.handleAboutConfigGet(w, r)
	case "notice_get":
		setCORS(w.Header())
		rt.handleNoticeGet(w, r)
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
