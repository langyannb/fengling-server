package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 4 私聊 action 的原生实现，逐字对齐 api.php:614-828。
//
// 复刻要点（改这里之前先读 api.php 原文，顺序错了客户端看到的第一条错误就变了）：
//   - pm_send 的顺序是「建会话 → 限速 → INSERT → 更新会话 → notify_merge → 响应」，
//     建会话**先于**限速：被限速拒绝时库里也会留下一个空会话（PHP 就是这个行为）。
//   - video_meta_params() 在 pm_send 里**无条件**生效，且早于「消息内容不能为空」。
//   - 空集合一律输出 []（PHP 的空数组 json_encode 结果），不许输出 null；
//     只有 pm_conversations[].last / pm_messages 的 other 允许为 null。

// pmOtherID 对齐 api.php:4248 pm_other_id()：取会话里的对方 id。
func pmOtherID(conv store.Row, meID int64) int64 {
	if rowInt(conv, "user_a", 0) == meID {
		return rowInt(conv, "user_b", 0)
	}
	return rowInt(conv, "user_a", 0)
}

// pmMsgPublic 对齐 api.php:4254 pm_msg_public()：私聊消息转客户端结构。
// 撤回后内容与媒体字段整体归零；msg_type 为空但有 video 时按 'video' 兜底。
func pmMsgPublic(m store.Row, meID int64) *phpjson.O {
	recalled := rowInt(m, "is_recalled", 0) == 1
	content := rowStr(m, "content", "")
	image := rowStr(m, "image", "")
	imgW := rowInt(m, "image_w", 0)
	imgH := rowInt(m, "image_h", 0)
	vurl := rowStr(m, "video", "")
	vW := rowInt(m, "video_w", 0)
	vH := rowInt(m, "video_h", 0)
	vDur := rowInt(m, "video_duration", 0)
	vSize := rowInt(m, "video_size", 0)
	mtype := rowStr(m, "msg_type", "")
	if mtype == "" && vurl != "" {
		mtype = "video"
	}
	if recalled {
		content, image, vurl, mtype = "", "", "", ""
		imgW, imgH, vW, vH, vDur, vSize = 0, 0, 0, 0, 0, 0
	}
	return phpjson.New().
		Set("id", rowInt(m, "id", 0)).
		Set("conv_id", rowInt(m, "conv_id", 0)).
		Set("user_id", rowInt(m, "from_user", 0)).
		Set("to_user", rowInt(m, "to_user", 0)).
		Set("content", content).
		Set("image", image).
		Set("image_w", imgW).
		Set("image_h", imgH).
		Set("msg_type", mtype).
		Set("video", vurl).
		Set("video_w", vW).
		Set("video_h", vH).
		Set("video_duration", vDur).
		Set("video_size", vSize).
		Set("is_recalled", boolToInt(recalled)).
		Set("mine", boolToInt(rowInt(m, "from_user", 0) == meID)).
		Set("created_at", rowStr(m, "created_at", ""))
}

// pmUnreadMap 对齐 api.php:4282 pm_unread_map()：我的私聊未读。
// 查询失败按「没有未读」处理（PHP 是 try/catch 吞掉），只记一条 Warn。
func (rt *Router) pmUnreadMap(c *Ctx, meID int64) map[int64]unreadInfo {
	out := map[int64]unreadInfo{}
	rows, err := rt.accounts().QueryAll(c.R.Context(), `SELECT m.conv_id, COUNT(*) AS c, MIN(m.id) AS first_id
            FROM social_pm_messages m
            LEFT JOIN social_pm_reads r ON r.conv_id = m.conv_id AND r.user_id = ?
            WHERE m.to_user = ? AND m.is_recalled = 0 AND m.id > COALESCE(r.last_read_id, 0)
            GROUP BY m.conv_id`, meID, meID)
	if err != nil {
		c.Log().Warn("查询私聊未读失败, 按无未读处理（PHP 此处 try/catch 吞掉）", "err", err)
		return out
	}
	for _, row := range rows {
		out[rowInt(row, "conv_id", 0)] = unreadInfo{
			count:   rowInt(row, "c", 0),
			firstID: rowInt(row, "first_id", 0),
		}
	}
	return out
}

// ---------- pm_conversations (api.php:614) ----------

func (rt *Router) handlePmConversations(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "pm_conversations")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	unreadMap := rt.pmUnreadMap(c, meID)

	rows, err := rt.accounts().QueryAll(c.R.Context(), `SELECT c.*,
                    ua.id AS a_id, ua.username AS a_username, ua.nickname AS a_nickname, ua.avatar AS a_avatar, ua.tags AS a_tags,
                    ub.id AS b_id, ub.username AS b_username, ub.nickname AS b_nickname, ub.avatar AS b_avatar, ub.tags AS b_tags
                FROM social_pms c
                INNER JOIN users ua ON ua.id = c.user_a
                INNER JOIN users ub ON ub.id = c.user_b
                WHERE c.user_a = ? OR c.user_b = ?
                ORDER BY c.last_message_id DESC, c.id DESC LIMIT 200`, meID, meID)
	if err != nil {
		c.dbError(err)
		return
	}

	list := make([]any, 0, len(rows))
	for _, conv := range rows {
		// 对方资料取自已 JOIN 出来的别名列，再交给 user_brief 归一化（与 PHP 拼数组一致）。
		other := store.Row{}
		if rowInt(conv, "user_a", 0) == meID {
			other["id"] = rowRaw(conv, "b_id")
			other["username"] = rowRaw(conv, "b_username")
			other["nickname"] = rowRaw(conv, "b_nickname")
			other["avatar"] = rowRaw(conv, "b_avatar")
			other["tags"] = rowRaw(conv, "b_tags")
		} else {
			other["id"] = rowRaw(conv, "a_id")
			other["username"] = rowRaw(conv, "a_username")
			other["nickname"] = rowRaw(conv, "a_nickname")
			other["avatar"] = rowRaw(conv, "a_avatar")
			other["tags"] = rowRaw(conv, "a_tags")
		}
		lastID := rowInt(conv, "last_message_id", 0)
		var last any // 没有最后一条消息时输出 null（不是 []）
		if lastID > 0 {
			m, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pm_messages WHERE id = ?", lastID)
			if err != nil {
				c.dbError(err)
				return
			}
			if m != nil {
				last = pmMsgPublic(m, meID)
			}
		}
		cid := rowInt(conv, "id", 0)
		u := unreadMap[cid]
		list = append(list, phpjson.New().
			Set("conv_id", cid).
			Set("user", userBrief(other)).
			Set("last", last).
			Set("last_time", rowStr(conv, "last_at", "")).
			Set("unread", u.count).
			Set("first_unread_id", u.firstID))
	}

	totalUnread := int64(0)
	for _, v := range unreadMap {
		totalUnread += v.count
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total_unread", totalUnread), 0, "ok", 0)
}

// ---------- pm_messages (api.php:655) ----------

func (rt *Router) handlePmMessages(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "pm_messages")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	cid := c.ParamInt("conv_id", 0)
	otherID := c.ParamInt("user_id", 0)
	if cid <= 0 && otherID > 0 {
		cid = rt.pmConvID(c, meID, otherID, false)
	}
	if cid <= 0 {
		// 提前返回：other 是 null、list 是 []（两者别混）。
		c.JSON(phpjson.New().
			Set("conv_id", 0).
			Set("other", nil).
			Set("list", []any{}).
			Set("has_more_before", 0).
			Set("unread", 0).
			Set("first_unread_id", 0).
			Set("my_id", meID), 0, "ok", 0)
		return
	}
	conv, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pms WHERE id = ?", cid)
	if err != nil {
		c.dbError(err)
		return
	}
	if conv == nil || (rowInt(conv, "user_a", 0) != meID && rowInt(conv, "user_b", 0) != meID) {
		c.Error("会话不存在", 1)
		return
	}
	otherID = pmOtherID(conv, meID)
	o, err := rt.accounts().QueryRow(c.R.Context(),
		"SELECT id, username, nickname, avatar, bio, role, created_at, tags FROM users WHERE id = ?", otherID)
	if err != nil {
		c.dbError(err)
		return
	}
	if o == nil {
		o = store.Row{} // PHP 是 `$st->fetch() ?: []`
	}

	limit := c.ParamInt("limit", 30)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	before := c.ParamInt("before_id", 0)
	after := c.ParamInt("after_id", 0)
	around := c.ParamInt("around_id", 0)
	// 优先级：around > before > after > 默认（与 PHP 的 if/elseif 链一致）。
	var rows []store.Row
	switch {
	case around > 0:
		rows, err = rt.accounts().QueryAll(c.R.Context(),
			"SELECT * FROM social_pm_messages WHERE conv_id = ? AND id <= ? ORDER BY id DESC LIMIT "+phpNum(limit), cid, around)
		rows = reverseRows(rows)
	case before > 0:
		rows, err = rt.accounts().QueryAll(c.R.Context(),
			"SELECT * FROM social_pm_messages WHERE conv_id = ? AND id < ? ORDER BY id DESC LIMIT "+phpNum(limit), cid, before)
		rows = reverseRows(rows)
	case after > 0:
		rows, err = rt.accounts().QueryAll(c.R.Context(),
			"SELECT * FROM social_pm_messages WHERE conv_id = ? AND id > ? ORDER BY id ASC LIMIT "+phpNum(limit), cid, after)
	default:
		rows, err = rt.accounts().QueryAll(c.R.Context(),
			"SELECT * FROM social_pm_messages WHERE conv_id = ? ORDER BY id DESC LIMIT "+phpNum(limit), cid)
		rows = reverseRows(rows)
	}
	if err != nil {
		c.dbError(err)
		return
	}

	firstID := int64(0)
	if len(rows) > 0 {
		firstID = rowInt(rows[0], "id", 0)
	}
	hasMore := int64(0)
	if firstID > 0 {
		v, hasRow, err := rt.accounts().QueryValue(c.R.Context(),
			"SELECT COUNT(*) FROM social_pm_messages WHERE conv_id = ? AND id < ?", cid, firstID)
		if err != nil {
			c.dbError(err)
			return
		}
		if intValue(v, hasRow) > 0 {
			hasMore = 1
		}
	}

	unreadMap := rt.pmUnreadMap(c, meID)
	u := unreadMap[cid]
	list := make([]any, 0, len(rows))
	for _, m := range rows {
		list = append(list, pmMsgPublic(m, meID))
	}
	nickname := rowStr(o, "nickname", "")
	if nickname == "" {
		nickname = rowStr(o, "username", "")
	}
	c.JSON(phpjson.New().
		Set("conv_id", cid).
		Set("other", phpjson.New().
			Set("id", rowInt(o, "id", otherID)).
			Set("username", rowStr(o, "username", "")).
			Set("nickname", nickname).
			Set("avatar", rowStr(o, "avatar", "")).
			Set("bio", rowStr(o, "bio", "")).
			Set("role", rowStr(o, "role", "user")).
			Set("created_at", rowStr(o, "created_at", "")).
			Set("tags", userTagsArr(o))).
		Set("list", list).
		Set("has_more_before", hasMore).
		Set("unread", u.count).
		Set("first_unread_id", u.firstID).
		Set("my_id", meID), 0, "ok", 0)
}

// ---------- pm_send (api.php:721) ----------

func (rt *Router) handlePmSend(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "pm_send")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	// 死代码也照抄：current_user() 的 SQL 已经过滤 is_active = 1，这里永远走不到。
	if rowInt(me, "is_active", 0) != 1 {
		c.Error("你已被封禁", 403)
		return
	}
	meID := rowInt(me, "id", 0)
	toID := c.ParamInt("to_user", 0)
	cid := c.ParamInt("conv_id", 0)
	if toID <= 0 && cid > 0 {
		conv, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pms WHERE id = ?", cid)
		if err != nil {
			c.dbError(err)
			return
		}
		if conv == nil || (rowInt(conv, "user_a", 0) != meID && rowInt(conv, "user_b", 0) != meID) {
			c.Error("会话不存在", 1)
			return
		}
		toID = pmOtherID(conv, meID)
	}
	if toID <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if toID == meID {
		c.Error("不能给自己发私聊", 1)
		return
	}
	// 全站禁言（group_id = 0）对私聊同样生效；管理员不受限。
	if rowStr(me, "role", "") != "admin" {
		muteState, muted := rt.userMuteState(c, meID, 0)
		if muted {
			leftTxt := muteLeftText(muteState)
			why := phpTrim(rowStr(muteState, "reason", ""))
			msg := "你已被禁言"
			if leftTxt != "" {
				msg += " (" + leftTxt + ")"
			}
			if why != "" {
				msg += ", 原因: " + why
			}
			c.Error(msg, 403)
			return
		}
	}
	target, err := rt.accounts().QueryRow(c.R.Context(),
		"SELECT id, username, nickname, avatar FROM users WHERE id = ? AND is_active = 1", toID)
	if err != nil {
		c.dbError(err)
		return
	}
	if target == nil {
		c.Error("对方不存在或已被封禁", 1)
		return
	}

	content := phpTrim(c.ParamStr("content", ""))
	image := phpTrim(c.ParamStr("image", ""))
	if image != "" && !strings.HasPrefix(image, rt.env.Cfg.S3PublicURL+"/chat/") {
		c.Error("图片地址不合法", 1)
		return
	}
	imgW := c.ParamInt("image_w", 0)
	if imgW < 0 {
		imgW = 0
	}
	imgH := c.ParamInt("image_h", 0)
	if imgH < 0 {
		imgH = 0
	}
	video := phpTrim(c.ParamStr("video", ""))
	if video != "" && !strings.HasPrefix(video, rt.env.Cfg.S3PublicURL+"/chat/") {
		c.Error("视频地址不合法", 1)
		return
	}
	if video != "" && rt.socialVideoConfig(c).Enabled != 1 {
		c.Error("视频消息功能未开启", 1)
		return
	}
	// 无条件校验（纯文字消息带了越界的 video_* 也会报错）——照抄 PHP，别「顺手修好」。
	vidW, vidH, vidDur, vidSize, okVideo := videoMetaParams(c)
	if !okVideo {
		return
	}
	hasVideoCol := rt.hasColumn(c, "social_pm_messages", "video")
	if video != "" && !hasVideoCol {
		c.Error("服务端未完成视频迁移, 请联系管理员", 1)
		return
	}
	msgType := ""
	if video != "" {
		msgType = "video"
	}
	if content == "" && image == "" && video == "" {
		c.Error("消息内容不能为空", 1)
		return
	}
	if mbStrlen(content) > 500 {
		c.Error("消息不能超过 500 个字", 1)
		return
	}

	// 建会话**先于**限速：被限速拒绝时库里也会留下一个空会话（PHP 行为）。
	cid = rt.pmConvID(c, meID, toID, true)
	if cid <= 0 {
		c.Error("会话创建失败, 请稍后重试", 1)
		return
	}
	lastV, hasLast, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT created_at FROM social_pm_messages WHERE from_user = ? ORDER BY id DESC LIMIT 1", meID)
	if err != nil {
		c.dbError(err)
		return
	}
	if hasLast {
		lastStr := phpStr(lastV)
		if !phpFalsy(lastStr) {
			if ts, okParse := phpStrtotime(lastStr); okParse && ts > nowUnix()-2 {
				c.Error("发送太快了, 请稍后再试", 1)
				return
			}
		}
	}

	pcols := []string{"conv_id", "from_user", "to_user", "content", "image", "image_w", "image_h"}
	pvals := []any{cid, meID, toID, content, image, imgW, imgH}
	if hasVideoCol {
		pcols = append(pcols, "video", "video_w", "video_h", "video_duration", "video_size")
		pvals = append(pvals, video, vidW, vidH, vidDur, vidSize)
	}
	if rt.hasColumn(c, "social_pm_messages", "msg_type") {
		pcols = append(pcols, "msg_type")
		pvals = append(pvals, msgType)
	}
	pcols = append(pcols, "is_recalled")
	pvals = append(pvals, 0)
	ph := strings.TrimSuffix(strings.Repeat("?,", len(pcols)), ",")
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO social_pm_messages ("+strings.Join(pcols, ", ")+") VALUES ("+ph+")", pvals...)
	if err != nil {
		c.dbError(err)
		return
	}
	mid := res.LastInsertID
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE social_pms SET last_message_id = ?, last_at = NOW() WHERE id = ?", mid, cid); err != nil {
		c.dbError(err)
		return
	}
	myName := rowStr(me, "nickname", "")
	if myName == "" {
		myName = rowStr(me, "username", "")
	}
	brief := ""
	switch {
	case content != "":
		brief = mbSubstr(content, 0, 60)
	case video != "":
		brief = "[视频]"
	default:
		brief = "[图片]"
	}
	rt.notifyMerge(c, toID, myName+" 给你发来私聊", brief, "pm", "pm:"+phpNum(cid)+":"+phpNum(mid))
	// created_at 用 PHP 进程时钟 date()，不是 DB 值（契约 §1.4）。
	c.JSON(phpjson.New().
		Set("id", mid).
		Set("conv_id", cid).
		Set("created_at", phpDateTime(time.Now())), 0, "ok", 0)
}

// ---------- pm_read (api.php:792) ----------

func (rt *Router) handlePmRead(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "pm_read")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	meID := rowInt(me, "id", 0)
	cid := c.ParamInt("conv_id", 0)
	if cid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	conv, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pms WHERE id = ?", cid)
	if err != nil {
		c.dbError(err)
		return
	}
	if conv == nil || (rowInt(conv, "user_a", 0) != meID && rowInt(conv, "user_b", 0) != meID) {
		c.Error("会话不存在", 1)
		return
	}
	lastID := c.ParamInt("last_id", 0)
	if lastID <= 0 {
		v, hasRow, err := rt.accounts().QueryValue(c.R.Context(),
			"SELECT COALESCE(MAX(id), 0) FROM social_pm_messages WHERE conv_id = ?", cid)
		if err != nil {
			c.dbError(err)
			return
		}
		lastID = intValue(v, hasRow)
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		`INSERT INTO social_pm_reads (user_id, conv_id, last_read_id, updated_at) VALUES (?, ?, ?, NOW())
                ON DUPLICATE KEY UPDATE last_read_id = GREATEST(last_read_id, VALUES(last_read_id)), updated_at = NOW()`,
		meID, cid, lastID); err != nil {
		c.dbError(err)
		return
	}
	v, hasRow, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT last_read_id FROM social_pm_reads WHERE user_id = ? AND conv_id = ?", meID, cid)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().
		Set("ok", true).
		Set("conv_id", cid).
		Set("last_read_id", intValue(v, hasRow)), 0, "ok", 0)
}

// ---------- pm_recall (api.php:814) ----------

func (rt *Router) handlePmRecall(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "pm_recall")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	id := c.ParamInt("id", 0)
	m, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pm_messages WHERE id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	if m == nil {
		c.Error("消息不存在", 1)
		return
	}
	if rowInt(m, "is_recalled", 0) == 1 {
		c.Error("消息已经撤回了", 1)
		return
	}
	isAdmin := rowStr(me, "role", "") == "admin"
	mine := rowInt(m, "from_user", 0) == rowInt(me, "id", 0)
	if !isAdmin && !mine {
		c.Error("没有权限撤回这条消息", 403)
		return
	}
	if !isAdmin {
		ts, okParse := phpStrtotime(rowStr(m, "created_at", ""))
		if !okParse {
			ts = 0 // strtotime 失败时 PHP 拿 false 当 0 比较
		}
		if ts < nowUnix()-300 {
			c.Error("只能撤回 5 分钟内的消息", 1)
			return
		}
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE social_pm_messages SET is_recalled = 1, recalled_by = ?, recalled_at = NOW() WHERE id = ?",
		rowInt(me, "id", 0), id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true), 0, "ok", 0)
}
