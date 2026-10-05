package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
	"github.com/langyannb/fengling-server/goapi/internal/upload"
)

// 阶段 5B-1：后台社群 / 消息 / 用户管理 22 个 admin action 的原生实现。
//
// 逐字对齐 api.php:352-489、1609-1827、2230-2409 与共用助手（:3263 require_admin、
// :3377 user_public、:3550 social_group_public、:3801 notify_push、:4303 user_brief、
// :4372 notify_push_all、:3833/3849 标签、:4133/4141 settings）。
//
// 与全库现状一致：**零事务、零审计**（照抄 PHP，改进另立阶段）。
// 鉴权统一走 requireAdminRow（对齐 require_admin()），鉴权先于业务参数解析。

// notificationColumns 是 notifications 表的真实列序（tools/install_social.php:34-44）。
//
// admin_notification_list 在 PHP 里是 `SELECT n.*, u.nickname, u.username` 后**原样**
// json_out，键序 = 物理列序。store.Row 是 map（无列序），所以这里显式列出。
// ⚠️ 若线上给 notifications 加了新列，这里必须同步补上（已在回报里列为未覆盖项）。
var notificationColumns = []string{
	"id", "user_id", "title", "content", "type", "link", "is_read", "created_at",
}

// tagSplitRe 对齐 api.php:3851 `preg_split('/[,，、]+/u', $raw)`。
var tagSplitRe = regexp.MustCompile("[,，、]+")

// userTagsStr 对齐 api.php:3849 user_tags_str()：归一化后台标签串为逗号分隔串。
func userTagsStr(raw string) string {
	parts := tagSplitRe.Split(raw, -1)
	out := []string{}
	for _, t := range parts {
		t = phpTrim(t)
		if t == "" || mbStrlen(t) > 10 {
			continue
		}
		dup := false
		for _, x := range out {
			if x == t {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, t)
		}
		if len(out) >= 5 {
			break
		}
	}
	return strings.Join(out, ",")
}

// adminSettingGet 对齐 api.php:4133 setting_get()：无行或值为 NULL 时返回默认值。
func (rt *Router) adminSettingGet(c *Ctx, key, def string) (string, bool) {
	v, has, err := rt.accounts().QueryValue(c.R.Context(), "SELECT `value` FROM settings WHERE `key` = ?", key)
	if err != nil {
		c.dbError(err)
		return "", false
	}
	if !has || v == nil {
		return def, true
	}
	return phpStr(v), true
}

// adminSettingSet 对齐 api.php:4141 setting_set()。
func (rt *Router) adminSettingSet(c *Ctx, key, value string) bool {
	if _, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO settings (`key`, `value`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)",
		key, value); err != nil {
		c.dbError(err)
		return false
	}
	return true
}

// adminNotifyPushAll 对齐 api.php:4372 notify_push_all()：逐条 INSERT，成功计数，
// 单条失败静默丢弃。返回 (成功条数, 是否可继续)。
func (rt *Router) adminNotifyPushAll(c *Ctx, title, content, typ, link string) (int64, bool) {
	rows, err := rt.accounts().QueryAll(c.R.Context(), "SELECT id FROM users WHERE is_active = 1")
	if err != nil {
		c.dbError(err)
		return 0, false
	}
	if len(rows) == 0 {
		return 0, true
	}
	var n int64
	for _, row := range rows {
		if _, err := rt.accounts().Exec(c.R.Context(),
			"INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)",
			rowInt(row, "id", 0), title, content, typ, link); err != nil {
			c.Log().Error("notify_push_all 单条插入失败（PHP 此处静默丢弃）", "err", err)
			continue
		}
		n++
	}
	return n, true
}

// decodeTagPresets 对齐 `json_decode(setting_get('user_tag_presets','[]'), true)`：
// 非数组（含 null / 标量）→ []；关联数组 → array_values。
func decodeTagPresets(raw string) []any {
	var arr []any
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		if arr == nil {
			return []any{}
		}
		return arr
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		out := []any{}
		for _, v := range m {
			out = append(out, v)
		}
		return out
	}
	return []any{}
}

// ---------- admin_users (api.php:352) ----------

func (rt *Router) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_users")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	size := c.paramSetInt("page_size", 20)
	if size < 1 {
		size = 1
	}
	if size > 100 {
		size = 100
	}
	kw := phpTrim(c.paramSetStr("keyword", ""))
	role := c.paramSetStr("role", "")
	active := c.paramSetStr("is_active", "")

	where := []string{}
	args := []any{}
	if kw != "" {
		where = append(where, "(username LIKE ? OR nickname LIKE ? OR email LIKE ? OR tags LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like, like)
	}
	if role == "user" || role == "admin" {
		where = append(where, "role = ?")
		args = append(args, role)
	}
	// PHP 里 is_active 白名单只认字符串 '0' / '1'（数字 0/1 会被 (string) 强转后同样命中）。
	if active == "0" || active == "1" {
		where = append(where, "is_active = ?")
		args = append(args, atoiPrefix(active))
	}
	wsql := ""
	if len(where) > 0 {
		wsql = " WHERE " + strings.Join(where, " AND ")
	}

	v, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM users"+wsql, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, true)

	off := (page - 1) * size
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT * FROM users"+wsql+" ORDER BY id ASC LIMIT "+phpNum(off)+", "+phpNum(size), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	ls := newLotteryScope(rt, c)
	list := []any{}
	for _, u := range rows {
		item := userPublic(u)
		item.Set("lottery_quota", rowInt(u, "lottery_quota", -1))
		drawn, derr := ls.drawnCount(rowInt(u, "id", 0))
		if derr != nil {
			c.dbError(derr)
			return
		}
		item.Set("lottery_drawn", drawn)
		left, lerr := ls.leftFor(u)
		if lerr != nil {
			c.dbError(lerr)
			return
		}
		item.Set("lottery_left", left)
		list = append(list, item)
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", total).
		Set("page", page).
		Set("page_size", size), 0, "ok", 0)
}

// ---------- admin_user_save (api.php:386) ----------

func (rt *Router) handleAdminUserSave(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_user_save")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	self, ok := rt.requireUser(c)
	if !ok {
		return
	}
	id := c.paramSetInt("id", 0)
	username := phpTrim(c.paramSetStr("username", ""))
	password := c.paramSetStr("password", "")
	nickname := phpTrim(c.paramSetStr("nickname", ""))
	email := phpTrim(c.paramSetStr("email", ""))
	bio := phpTrim(c.paramSetStr("bio", ""))
	role := "user"
	if c.paramSetStr("role", "user") == "admin" {
		role = "admin"
	}
	active := int64(0)
	if c.paramSetInt("is_active", 1) != 0 {
		active = 1
	}

	if username != "" && !usernameRe.MatchString(username) {
		c.Error("用户名只能包含字母数字下划线(3-20位)", 1)
		return
	}
	if email != "" && !phpValidateEmail(email) {
		c.Error("邮箱格式不正确", 1)
		return
	}
	if nickname == "" && username != "" {
		nickname = username
	}
	if mbStrlen(nickname) > 20 {
		c.Error("昵称不能超过 20 个字", 1)
		return
	}
	if mbStrlen(bio) > 100 {
		c.Error("简介不能超过 100 个字", 1)
		return
	}

	dup, _, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT id FROM users WHERE username = ? AND id <> ?", username, id)
	if err != nil {
		c.dbError(err)
		return
	}
	if dup != nil {
		c.Error("用户名已存在", 1)
		return
	}
	if email != "" {
		dup, _, err = rt.accounts().QueryValue(c.R.Context(),
			"SELECT id FROM users WHERE email = ? AND id <> ?", email, id)
		if err != nil {
			c.dbError(err)
			return
		}
		if dup != nil {
			c.Error("该邮箱已被注册", 1)
			return
		}
	}

	if id > 0 {
		old, oerr := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM users WHERE id = ?", id)
		if oerr != nil {
			c.dbError(oerr)
			return
		}
		if old == nil {
			c.Error("用户不存在", 1)
			return
		}
		if rowInt(self, "id", 0) == id && (role != "admin" || active == 0) {
			c.Error("不能修改自己的角色, 也不能禁用自己的账号", 1)
			return
		}
		if username == "" {
			username = rowStr(old, "username", "")
		}
		if nickname == "" {
			nickname = rowStr(old, "nickname", "")
		}
		if email == "" {
			email = rowStr(old, "email", "")
		}
		if bio == "" {
			bio = rowStr(old, "bio", "")
		}
		if !c.HasParam("role") {
			role = rowStr(old, "role", "user")
		}
		if !c.HasParam("is_active") {
			if rowInt(old, "is_active", 1) != 0 {
				active = 1
			} else {
				active = 0
			}
		}
		if username == "" || !usernameRe.MatchString(username) {
			c.Error("用户名只能包含字母数字下划线(3-20位)", 1)
			return
		}
		var emailVal any
		if email != "" {
			emailVal = email
		}
		fields := []string{"username = ?", "nickname = ?", "email = ?", "role = ?", "is_active = ?", "bio = ?"}
		args := []any{username, nickname, emailVal, role, active, bio}
		if password != "" {
			if len(password) < 6 {
				c.Error("密码至少 6 位", 1)
				return
			}
			h, herr := hashPassword(password)
			if herr != nil {
				c.dbError(herr)
				return
			}
			fields = append(fields, "password = ?")
			args = append(args, h)
		}
		if email != "" && email != rowStr(old, "email", "") {
			fields = append(fields, "email_verified = ?")
			args = append(args, int64(0))
		}
		// 邮箱改成 QQ 邮箱且该用户还没有头像: 自动补一个 QQ 头像（avatar 排在 id 之前）。
		if phpTrim(rowStr(old, "avatar", "")) == "" {
			if qa := qqAvatarFromEmail(email); qa != "" {
				fields = append(fields, "avatar = ?")
				args = append(args, qa)
			}
		}
		args = append(args, id)
		if _, werr := rt.accounts().Exec(c.R.Context(),
			"UPDATE users SET "+strings.Join(fields, ", ")+" WHERE id = ?", args...); werr != nil {
			c.dbError(werr)
			return
		}
		// 由封禁改为启用时推一条通知（PHP 用 (int)$old['is_active'] === 0）。
		if active == 1 && rowInt(old, "is_active", 0) == 0 {
			rt.notifyPush(c, id, "账号已恢复", "你的账号已由管理员解除封禁, 现在可以正常登录使用了。", "admin")
		}
	} else {
		if len(password) < 6 {
			c.Error("密码至少 6 位", 1)
			return
		}
		var emailVal any
		if email != "" {
			emailVal = email
		}
		h, herr := hashPassword(password)
		if herr != nil {
			c.dbError(herr)
			return
		}
		res, ierr := rt.accounts().Exec(c.R.Context(),
			"INSERT INTO users (username, password, nickname, email, email_verified, avatar, role, is_active, bio) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)",
			username, h, nickname, emailVal, qqAvatarFromEmail(email), role, active, bio)
		if ierr != nil {
			c.dbError(ierr)
			return
		}
		id = res.LastInsertID
	}

	final, ferr := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM users WHERE id = ?", id)
	if ferr != nil {
		c.dbError(ferr)
		return
	}
	c.JSON(userPublic(final), 0, "ok", 0)
}

// ---------- admin_user_delete (api.php:473) ----------

func (rt *Router) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_user_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	self, ok := rt.requireUser(c)
	if !ok {
		return
	}
	id := c.paramSetInt("id", 0)
	if id <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if rowInt(self, "id", 0) == id {
		c.Error("不能删除自己的账号", 1)
		return
	}
	u, err := rt.accounts().QueryRow(c.R.Context(), "SELECT role FROM users WHERE id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	if u == nil {
		c.Error("用户不存在", 1)
		return
	}
	if rowStr(u, "role", "") == "admin" {
		v, _, cerr := rt.accounts().QueryValue(c.R.Context(),
			"SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = 1")
		if cerr != nil {
			c.dbError(cerr)
			return
		}
		if intValue(v, true) <= 1 {
			c.Error("至少保留一个管理员", 1)
			return
		}
	}
	if _, derr := rt.accounts().Exec(c.R.Context(), "DELETE FROM sessions WHERE user_id = ?", id); derr != nil {
		c.dbError(derr)
		return
	}
	if _, derr := rt.accounts().Exec(c.R.Context(), "DELETE FROM users WHERE id = ?", id); derr != nil {
		c.dbError(derr)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- admin_groups (api.php:1609) ----------

func (rt *Router) handleAdminGroups(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_groups")
	admin, ok := rt.requireAdminRow(c)
	if !ok {
		return
	}
	s := rt.newSocialScope(c)
	rows, err := s.db().QueryAll(c.R.Context(), `SELECT g.*,
                    (SELECT COUNT(DISTINCT m.user_id) FROM social_messages m WHERE m.group_id = g.id) AS member_count,
                    (SELECT COUNT(*) FROM social_messages m WHERE m.group_id = g.id) AS message_count
                FROM social_groups g ORDER BY g.sort_order ASC, g.id ASC`)
	if err != nil {
		c.dbError(err)
		return
	}
	meID := rowInt(admin, "id", 0)
	list := []any{}
	for _, g := range rows {
		// PHP array_map('social_group_public', $rows)：muted/unread/first_unread=0、last=null、
		// mention 全 0；member_count 由 social_member_count() 覆盖子查询列。
		list = append(list, s.groupPublic(g, false, 0, 0, nil, mentionInfo{}, meID))
	}
	c.JSON(phpjson.New().Set("list", list), 0, "ok", 0)
}

// ---------- admin_group_save (api.php:1617) ----------

func (rt *Router) handleAdminGroupSave(w http.ResponseWriter, r *http.Request) {
	// multipart 走流式解析（icon_file），非 multipart 走普通 Ctx（JSON body）。
	isMultipart := strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data")
	var c *Ctx
	var iconFile *upload.FilePart
	if isMultipart {
		c = rt.env.newCtxStreaming(w, r, "admin_group_save")
		res, err := upload.ReadMultipart(c.R, "icon_file", rt.env.Cfg.UploadTmpDir, 0)
		if err != nil {
			if !errors.Is(err, upload.ErrNotMultipart) {
				c.Log().Warn("multipart 解析中断", "err", err)
				c.Error("读取文件失败", 1)
				return
			}
		} else {
			c.setMultipart(res.Fields)
			iconFile = res.File
			if iconFile != nil {
				defer os.Remove(iconFile.Path)
			}
		}
	} else {
		c = rt.env.newCtx(w, r, "admin_group_save")
	}
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}

	id := c.paramSetInt("id", 0)
	name := phpTrim(c.paramSetStr("name", ""))
	if id > 0 && !c.HasParam("name") {
		old, err := rt.accounts().QueryRow(c.R.Context(), "SELECT name FROM social_groups WHERE id = ?", id)
		if err != nil {
			c.dbError(err)
			return
		}
		if old != nil {
			name = rowStr(old, "name", "")
		}
	}
	if name == "" {
		c.Error("群组名称不能为空", 1)
		return
	}
	if mbStrlen(name) > 20 {
		c.Error("群组名称不能超过 20 个字", 1)
		return
	}

	icon := phpTrim(c.paramSetStr("icon", ""))
	if iconFile != nil {
		data, rerr := os.ReadFile(iconFile.Path)
		if rerr != nil || len(data) == 0 {
			c.Error("头像文件读取失败, 请重试", 1)
			return
		}
		info, perr := upload.ProbeImage(data)
		switch {
		case errors.Is(perr, upload.ErrNotImage):
			c.Error("这不是一张有效的图片", 1)
			return
		case perr != nil:
			c.Error("只支持 jpg / png / gif / webp 图片", 1)
			return
		}
		if rt.env.S3 == nil {
			c.Log().Error("S3 客户端未初始化")
			c.Error("头像上传失败, 请稍后重试", 1)
			return
		}
		out := upload.CompressImage(data, info.Ext, 256, 88)
		key := upload.S3Key("group_icons", out.Ext)
		if err := rt.env.S3.PutBytes(c.R.Context(), key, out.MIME, out.Data); err != nil {
			c.Log().Warn("对象存储上传失败", "key", key, "err", err)
			c.Error("头像上传失败, 请稍后重试", 1)
			return
		}
		icon = rt.env.S3.ObjectURL(key)
	}

	desc := phpTrim(c.paramSetStr("description", ""))
	if mbStrlen(desc) > 100 {
		c.Error("简介不能超过 100 个字", 1)
		return
	}
	sortOrder := c.paramSetInt("sort_order", 0)
	active := int64(0)
	if c.paramSetInt("is_active", 1) == 1 {
		active = 1
	}
	hasAllMuted := c.HasParam("all_muted")
	allMuted := int64(0)
	if c.paramSetInt("all_muted", 0) == 1 {
		allMuted = 1
	}
	notice := phpTrim(c.paramSetStr("notice", ""))
	if id > 0 && !c.HasParam("notice") {
		old, err := rt.accounts().QueryRow(c.R.Context(), "SELECT notice FROM social_groups WHERE id = ?", id)
		if err != nil {
			c.dbError(err)
			return
		}
		if old != nil {
			notice = rowStr(old, "notice", "")
		}
	}

	dup, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT id FROM social_groups WHERE name = ? AND id <> ?", name, id)
	if err != nil {
		c.dbError(err)
		return
	}
	if dup != nil {
		c.Error("已存在同名群组", 1)
		return
	}

	if id > 0 {
		if _, err := rt.accounts().Exec(c.R.Context(),
			"UPDATE social_groups SET name = ?, icon = ?, description = ?, sort_order = ?, is_active = ?, notice = ? WHERE id = ?",
			name, icon, desc, sortOrder, active, notice, id); err != nil {
			c.dbError(err)
			return
		}
		if hasAllMuted {
			if _, err := rt.accounts().Exec(c.R.Context(),
				"UPDATE social_groups SET all_muted = ? WHERE id = ?", allMuted, id); err != nil {
				c.dbError(err)
				return
			}
		}
		c.JSON(phpjson.New().Set("id", id), 0, "ok", 0)
		return
	}
	res, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO social_groups (name, icon, description, sort_order, is_active, notice) VALUES (?, ?, ?, ?, ?, ?)",
		name, icon, desc, sortOrder, active, notice)
	if err != nil {
		c.dbError(err)
		return
	}
	newGid := res.LastInsertID
	if hasAllMuted {
		if _, err := rt.accounts().Exec(c.R.Context(),
			"UPDATE social_groups SET all_muted = ? WHERE id = ?", allMuted, newGid); err != nil {
			c.dbError(err)
			return
		}
	}
	c.JSON(phpjson.New().Set("id", newGid), 0, "ok", 0)
}

// ---------- admin_group_delete (api.php:1680) ----------

func (rt *Router) handleAdminGroupDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_group_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := c.paramSetInt("id", 0)
	if id <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_messages WHERE group_id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_groups WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true), 0, "ok", 0)
}

// ---------- admin_group_members (api.php:1689) ----------

func (rt *Router) handleAdminGroupMembers(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_group_members")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := rt.newSocialScope(c)
	gid := c.paramSetInt("group_id", 0)
	if gid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	kw := phpTrim(c.paramSetStr("keyword", ""))
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 30)
	if ps < 1 {
		ps = 1
	}
	if ps > 60 {
		ps = 60
	}
	off := (page - 1) * ps

	where := []string{"m.group_id = ?"}
	args := []any{gid}
	if kw != "" {
		where = append(where, "(u.nickname LIKE ? OR u.username LIKE ?)")
		args = append(args, "%"+kw+"%", "%"+kw+"%")
	}
	cond := " WHERE " + strings.Join(where, " AND ")

	v, _, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM social_group_members m JOIN users u ON u.id = m.user_id"+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, true)

	// MySQL 的 FIELD(m.role,'owner','admin','member') 用 CASE WHEN 重写（其余角色排最后）。
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT u.*, m.role AS group_role, m.joined_at\n"+
			"                                   FROM social_group_members m JOIN users u ON u.id = m.user_id"+
			cond+" ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END, m.joined_at ASC, m.id ASC\n"+
			"                          LIMIT "+phpNum(ps)+" OFFSET "+phpNum(off), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, u := range rows {
		item := userBrief(u)
		groupRole := rowStr(u, "group_role", "")
		item.Set("role", groupRole)
		item.Set("group_role", groupRole)
		item.Set("joined_at", rowStr(u, "joined_at", ""))
		item.Set("global_role", rowStr(u, "role", "user"))
		isAdmin := int64(0)
		if rowStr(u, "role", "") == "admin" {
			isAdmin = 1
		}
		item.Set("is_admin", isAdmin)
		list = append(list, item)
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("page", page).
		Set("page_size", ps).
		Set("total", total), 0, "ok", 0)
}

// ---------- admin_group_member_remove (api.php:1726) ----------

func (rt *Router) handleAdminGroupMemberRemove(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_group_member_remove")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	s := rt.newSocialScope(c)
	gid := c.paramSetInt("group_id", 0)
	uid := c.paramSetInt("user_id", 0)
	if gid <= 0 || uid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, ok := s.groupOr404(gid, false); !ok {
		return
	}
	res, err := rt.accounts().Exec(c.R.Context(),
		"DELETE FROM social_group_members WHERE group_id = ? AND user_id = ?", gid, uid)
	if err != nil {
		c.dbError(err)
		return
	}
	if res.RowsAffected <= 0 {
		c.Error("成员不存在", 1)
		return
	}
	c.JSON(phpjson.New().
		Set("group_id", gid).
		Set("user_id", uid).
		Set("removed", int64(1)).
		Set("member_count", s.memberCount(gid)), 0, "ok", 0)
}

// ---------- admin_social_messages (api.php:1743) ----------

func (rt *Router) handleAdminSocialMessages(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_social_messages")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 100 {
		ps = 100
	}
	gid := c.paramSetInt("group_id", 0)
	kw := phpTrim(c.paramSetStr("keyword", ""))

	where := []string{}
	args := []any{}
	if gid > 0 {
		where = append(where, "m.group_id = ?")
		args = append(args, gid)
	}
	if kw != "" {
		where = append(where, "m.content LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}

	v, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM social_messages m "+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, true)

	off := (page - 1) * ps
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT m.*, u.nickname, u.username, u.avatar, u.role, g.name AS group_name\n"+
			"                FROM social_messages m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN social_groups g ON g.id = m.group_id\n"+
			"                "+cond+" ORDER BY m.id DESC LIMIT "+phpNum(ps)+" OFFSET "+phpNum(off), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		// PHP 先强制 group_name 为字符串、nickname 空则回落 username，再走 social_msg_public。
		row["group_name"] = rowStr(row, "group_name", "")
		if rowStr(row, "nickname", "") == "" {
			row["nickname"] = rowStr(row, "username", "")
		}
		list = append(list, socialMsgPublic(row))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", total).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- admin_social_message_delete (api.php:1768) ----------

func (rt *Router) handleAdminSocialMessageDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_social_message_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := c.paramSetInt("id", 0)
	if id <= 0 {
		c.Error("参数错误", 1)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_messages WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true), 0, "ok", 0)
}

// ---------- admin_social_message_clear (api.php:2397) ----------

func (rt *Router) handleAdminSocialMessageClear(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_social_message_clear")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	gid := c.paramSetInt("group_id", 0)
	kw := phpTrim(c.paramSetStr("keyword", ""))
	where := []string{}
	args := []any{}
	if gid > 0 {
		where = append(where, "group_id = ?")
		args = append(args, gid)
	}
	if kw != "" {
		where = append(where, "content LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	v, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM social_messages "+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	n := intValue(v, true)
	if n > 0 {
		if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_messages "+cond, args...); err != nil {
			c.dbError(err)
			return
		}
	}
	c.JSON(phpjson.New().Set("deleted", n), 0, "ok", 0)
}

// ---------- admin_notify_send (api.php:1776) ----------

func (rt *Router) handleAdminNotifySend(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_notify_send")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	title := phpTrim(c.paramSetStr("title", ""))
	content := phpTrim(c.paramSetStr("content", ""))
	typ := "admin"
	if c.paramSetStr("type", "admin") == "system" {
		typ = "system"
	}
	link := phpTrim(c.paramSetStr("link", ""))
	if title == "" {
		c.Error("标题不能为空", 1)
		return
	}
	if content == "" {
		c.Error("内容不能为空", 1)
		return
	}
	if mbStrlen(title) > 50 {
		c.Error("标题不能超过 50 个字", 1)
		return
	}
	target := c.paramSetAny("target", "all")
	var count int64
	if phpStr(target) == "all" || phpStr(target) == "" || phpInt(target) == 0 {
		n, ok := rt.adminNotifyPushAll(c, title, content, typ, link)
		if !ok {
			return
		}
		count = n
	} else {
		uid := phpInt(target)
		u, err := rt.accounts().QueryRow(c.R.Context(), "SELECT id FROM users WHERE id = ?", uid)
		if err != nil {
			c.dbError(err)
			return
		}
		if u == nil {
			c.Error("用户不存在", 1)
			return
		}
		rt.notifyPushLink(c, uid, title, content, typ, link)
		count = 1
	}
	c.JSON(phpjson.New().Set("ok", true).Set("count", count), 0, "ok", 0)
}

// ---------- admin_notification_list (api.php:1798) ----------

func (rt *Router) handleAdminNotificationList(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_notification_list")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 100 {
		ps = 100
	}
	kw := phpTrim(c.paramSetStr("keyword", ""))
	where := []string{}
	args := []any{}
	if kw != "" {
		where = append(where, "(n.title LIKE ? OR n.content LIKE ?)")
		args = append(args, "%"+kw+"%", "%"+kw+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	v, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM notifications n "+cond, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, true)

	off := (page - 1) * ps
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT n.*, u.nickname, u.username FROM notifications n LEFT JOIN users u ON u.id = n.user_id\n"+
			"                "+cond+" ORDER BY n.id DESC LIMIT "+phpNum(ps)+" OFFSET "+phpNum(off), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, row := range rows {
		// PHP 直接 json_out 原始关联数组：PDO 默认（模拟预处理）把整数也返回成字符串，
		// 因此这里对 n.* 列用 rowRaw（NULL→null，其余按 PHP (string) 输出）。
		item := phpjson.New()
		for _, col := range notificationColumns {
			item.Set(col, rowRaw(row, col))
		}
		item.Set("nickname", rowRaw(row, "nickname"))
		item.Set("username", rowRaw(row, "username"))
		userName := ""
		if rowStr(row, "nickname", "") != "" {
			userName = rowStr(row, "nickname", "")
		} else {
			userName = rowStr(row, "username", "")
		}
		item.Set("user_name", userName)
		list = append(list, item)
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", total).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- admin_notification_delete (api.php:1819) ----------

func (rt *Router) handleAdminNotificationDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_notification_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := c.paramSetInt("id", 0)
	if id <= 0 {
		if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM notifications"); err != nil {
			c.dbError(err)
			return
		}
		c.JSON(phpjson.New().Set("ok", true).Set("cleared", true), 0, "ok", 0)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM notifications WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true), 0, "ok", 0)
}

// ---------- admin_pm_conversations (api.php:2230) ----------

func (rt *Router) handleAdminPmConversations(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_pm_conversations")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 100 {
		ps = 100
	}
	kw := phpTrim(c.paramSetStr("keyword", ""))
	where := []string{}
	args := []any{}
	if kw != "" {
		where = append(where, "(ua.nickname LIKE ? OR ua.username LIKE ? OR ub.nickname LIKE ? OR ub.username LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like, like)
	}
	wc := ""
	if len(where) > 0 {
		wc = " WHERE " + strings.Join(where, " AND ")
	}
	v, _, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COUNT(*) FROM social_pms c\n"+
			"                INNER JOIN users ua ON ua.id = c.user_a INNER JOIN users ub ON ub.id = c.user_b"+wc, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, true)

	off := (page - 1) * ps
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT c.id, c.last_message_id, c.last_at, c.created_at,\n"+
			"                    ua.id AS a_id, ua.username AS a_username, ua.nickname AS a_nickname, ua.avatar AS a_avatar,\n"+
			"                    ub.id AS b_id, ub.username AS b_username, ub.nickname AS b_nickname, ub.avatar AS b_avatar,\n"+
			"                    (SELECT COUNT(*) FROM social_pm_messages m WHERE m.conv_id = c.id) AS message_count,\n"+
			"                    (SELECT m2.content FROM social_pm_messages m2 WHERE m2.conv_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_content,\n"+
			"                    (SELECT m3.image FROM social_pm_messages m3 WHERE m3.conv_id = c.id ORDER BY m3.id DESC LIMIT 1) AS last_image\n"+
			"                FROM social_pms c\n"+
			"                INNER JOIN users ua ON ua.id = c.user_a INNER JOIN users ub ON ub.id = c.user_b"+
			wc+" ORDER BY c.last_message_id DESC, c.id DESC LIMIT "+phpNum(off)+", "+phpNum(ps), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, cv := range rows {
		userA := store.Row{
			"id": rowInt(cv, "a_id", 0), "username": rowStr(cv, "a_username", ""),
			"nickname": rowRaw(cv, "a_nickname"), "avatar": rowStr(cv, "a_avatar", ""),
		}
		userB := store.Row{
			"id": rowInt(cv, "b_id", 0), "username": rowStr(cv, "b_username", ""),
			"nickname": rowRaw(cv, "b_nickname"), "avatar": rowStr(cv, "b_avatar", ""),
		}
		list = append(list, phpjson.New().
			Set("id", rowInt(cv, "id", 0)).
			Set("user_a", userBrief(userA)).
			Set("user_b", userBrief(userB)).
			Set("message_count", rowInt(cv, "message_count", 0)).
			Set("last_content", rowStr(cv, "last_content", "")).
			Set("last_image", rowStr(cv, "last_image", "")).
			Set("last_at", rowStr(cv, "last_at", "")).
			Set("created_at", rowStr(cv, "created_at", "")))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", total).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- admin_pm_messages (api.php:2272) ----------

func (rt *Router) handleAdminPmMessages(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_pm_messages")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	cid := c.paramSetInt("conv_id", 0)
	if cid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	conv, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pms WHERE id = ?", cid)
	if err != nil {
		c.dbError(err)
		return
	}
	if conv == nil {
		c.Error("会话不存在", 1)
		return
	}
	page := c.paramSetInt("page", 1)
	if page < 1 {
		page = 1
	}
	ps := c.paramSetInt("page_size", 20)
	if ps < 1 {
		ps = 1
	}
	if ps > 100 {
		ps = 100
	}
	kw := phpTrim(c.paramSetStr("keyword", ""))
	where := []string{"m.conv_id = ?"}
	args := []any{cid}
	if kw != "" {
		where = append(where, "m.content LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	wc := " WHERE " + strings.Join(where, " AND ")

	v, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM social_pm_messages m"+wc, args...)
	if err != nil {
		c.dbError(err)
		return
	}
	total := intValue(v, true)

	off := (page - 1) * ps
	rows, err := rt.accounts().QueryAll(c.R.Context(),
		"SELECT m.*, uf.nickname AS from_nickname, uf.username AS from_username,\n"+
			"                    ut.nickname AS to_nickname, ut.username AS to_username\n"+
			"                FROM social_pm_messages m\n"+
			"                LEFT JOIN users uf ON uf.id = m.from_user\n"+
			"                LEFT JOIN users ut ON ut.id = m.to_user"+
			wc+" ORDER BY m.id DESC LIMIT "+phpNum(off)+", "+phpNum(ps), args...)
	if err != nil {
		c.dbError(err)
		return
	}
	list := []any{}
	for _, m := range rows {
		fromName := rowStr(m, "from_nickname", "")
		if fromName == "" {
			fromName = rowStr(m, "from_username", "")
		}
		toName := rowStr(m, "to_nickname", "")
		if toName == "" {
			toName = rowStr(m, "to_username", "")
		}
		msgType := rowStr(m, "msg_type", "")
		if rowStr(m, "video", "") != "" {
			msgType = "video"
		}
		list = append(list, phpjson.New().
			Set("id", rowInt(m, "id", 0)).
			Set("conv_id", rowInt(m, "conv_id", 0)).
			Set("from_user", rowInt(m, "from_user", 0)).
			Set("to_user", rowInt(m, "to_user", 0)).
			Set("from_name", fromName).
			Set("to_name", toName).
			Set("content", rowStr(m, "content", "")).
			Set("image", rowStr(m, "image", "")).
			Set("msg_type", msgType).
			Set("video", rowStr(m, "video", "")).
			Set("is_recalled", rowInt(m, "is_recalled", 0)).
			Set("created_at", rowStr(m, "created_at", "")))
	}
	c.JSON(phpjson.New().
		Set("list", list).
		Set("total", total).
		Set("page", page).
		Set("page_size", ps), 0, "ok", 0)
}

// ---------- admin_pm_message_delete (api.php:2315) ----------

func (rt *Router) handleAdminPmMessageDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_pm_message_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	id := c.paramSetInt("id", 0)
	if id <= 0 {
		c.Error("参数错误", 1)
		return
	}
	m, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM social_pm_messages WHERE id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	if m == nil {
		c.Error("消息不存在", 1)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_pm_messages WHERE id = ?", id); err != nil {
		c.dbError(err)
		return
	}
	cid := rowInt(m, "conv_id", 0)
	v, _, err := rt.accounts().QueryValue(c.R.Context(),
		"SELECT COALESCE(MAX(id), 0) FROM social_pm_messages WHERE conv_id = ?", cid)
	if err != nil {
		c.dbError(err)
		return
	}
	newLast := intValue(v, true)
	var last string
	if newLast > 0 {
		lv, _, lerr := rt.accounts().QueryValue(c.R.Context(), "SELECT created_at FROM social_pm_messages WHERE id = ?", newLast)
		if lerr != nil {
			c.dbError(lerr)
			return
		}
		last = phpStr(lv)
	}
	var lastArg any
	if last != "" {
		lastArg = last
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE social_pms SET last_message_id = ?, last_at = ? WHERE id = ?", newLast, lastArg, cid); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("ok", true).Set("deleted", int64(1)), 0, "ok", 0)
}

// ---------- admin_pm_clear (api.php:2338) ----------

func (rt *Router) handleAdminPmClear(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_pm_clear")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	cid := c.paramSetInt("conv_id", 0)
	if cid <= 0 {
		c.Error("请选择会话", 1)
		return
	}
	v, _, err := rt.accounts().QueryValue(c.R.Context(), "SELECT COUNT(*) FROM social_pm_messages WHERE conv_id = ?", cid)
	if err != nil {
		c.dbError(err)
		return
	}
	n := intValue(v, true)
	if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_pm_messages WHERE conv_id = ?", cid); err != nil {
		c.dbError(err)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(),
		"UPDATE social_pms SET last_message_id = 0, last_at = NULL WHERE id = ?", cid); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("deleted", n), 0, "ok", 0)
}

// ---------- admin_user_tags_set (api.php:2350) ----------

func (rt *Router) handleAdminUserTagsSet(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_user_tags_set")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	uid := c.paramSetInt("user_id", 0)
	if uid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	str := userTagsStr(c.paramSetStr("tags", ""))
	u, err := rt.accounts().QueryRow(c.R.Context(), "SELECT id FROM users WHERE id = ?", uid)
	if err != nil {
		c.dbError(err)
		return
	}
	if u == nil {
		c.Error("用户不存在", 1)
		return
	}
	if _, err := rt.accounts().Exec(c.R.Context(), "UPDATE users SET tags = ? WHERE id = ?", str, uid); err != nil {
		c.dbError(err)
		return
	}
	var tags []string
	if str == "" {
		tags = []string{}
	} else {
		tags = strings.Split(str, ",")
	}
	c.JSON(phpjson.New().Set("user_id", uid).Set("tags", tags), 0, "ok", 0)
}

// ---------- admin_tags (api.php:2361) ----------

func (rt *Router) handleAdminTags(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_tags")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	raw, ok := rt.adminSettingGet(c, "user_tag_presets", "[]")
	if !ok {
		return
	}
	presets := decodeTagPresets(raw)
	used := []string{}
	rows, err := rt.accounts().QueryAll(c.R.Context(), "SELECT tags FROM users WHERE tags <> ''")
	if err != nil {
		c.dbError(err)
		return
	}
	for _, row := range rows {
		for _, t := range userTagsArr(row) {
			seen := false
			for _, x := range used {
				if x == t {
					seen = true
					break
				}
			}
			if !seen {
				used = append(used, t)
			}
		}
	}
	c.JSON(phpjson.New().Set("presets", presets).Set("used", used), 0, "ok", 0)
}

// ---------- admin_tag_preset_save (api.php:2373) ----------

func (rt *Router) handleAdminTagPresetSave(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_tag_preset_save")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	tag := phpTrim(c.paramSetStr("tag", ""))
	if tag == "" {
		c.Error("标签不能为空", 1)
		return
	}
	if mbStrlen(tag) > 10 {
		c.Error("标签不能超过 10 个字", 1)
		return
	}
	raw, ok := rt.adminSettingGet(c, "user_tag_presets", "[]")
	if !ok {
		return
	}
	presets := decodeTagPresets(raw)
	found := false
	for _, v := range presets {
		if s, isStr := v.(string); isStr && s == tag {
			found = true
			break
		}
	}
	if !found {
		if len(presets) >= 30 {
			c.Error("预设标签最多 30 个", 1)
			return
		}
		presets = append(presets, tag)
	}
	if !rt.adminSettingSet(c, "user_tag_presets", string(phpjson.Marshal(presets))) {
		return
	}
	c.JSON(phpjson.New().Set("presets", presets), 0, "ok", 0)
}

// ---------- admin_tag_preset_delete (api.php:2387) ----------

func (rt *Router) handleAdminTagPresetDelete(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "admin_tag_preset_delete")
	if _, ok := rt.requireAdminRow(c); !ok {
		return
	}
	tag := phpTrim(c.paramSetStr("tag", ""))
	raw, ok := rt.adminSettingGet(c, "user_tag_presets", "[]")
	if !ok {
		return
	}
	presets := decodeTagPresets(raw)
	out := []any{}
	for _, v := range presets {
		if s, isStr := v.(string); isStr && s == tag {
			continue
		}
		out = append(out, v)
	}
	if !rt.adminSettingSet(c, "user_tag_presets", string(phpjson.Marshal(out))) {
		return
	}
	c.JSON(phpjson.New().Set("presets", out), 0, "ok", 0)
}
