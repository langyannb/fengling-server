package httpapi

import (
	crand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/langyannb/fengling-server/goapi/internal/captcha"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 2：账号与鉴权 11 个 action 的原生实现。
//
// 逐条对齐 api.php:185-2418 / :287-349 / :493-611，包括：
//   - 每个报错的中文文案、JSON code 与（由 statusForCode 决定的）HTTP 状态码；
//   - JSON 字段名与顺序（走 phpjson 有序对象）；
//   - 参数读取顺序（Ctx.Param 复刻 config.php param()）；
//   - 「忽略异常」的分支（PHP 里 try/catch 包住的写库失败只记日志，不返回 500）。
//
// 刻意保留的 PHP 怪癖（不要「顺手修好」）：
//   - login 的密码为空判断是 `!$password`，所以字符串 "0" 也算空；
//   - register / send_code 的「验证码答错也作废」；
//   - logout 只看 param('token')，**不看 Authorization 头**；
//   - user_profile 允许游客访问（$me 为 null 时不报 401）。

// codeSentTTL 是 api.php:224 的 60 秒窗口（Redis 标记用，DB 才是真值）。
const codeSentTTL = 60 * time.Second

// usernameRe 对齐 api.php:248 的 `/^[A-Za-z0-9_]{3,20}$/`。
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// avatarURLRe 对齐 api.php:307 的 `#^https?://#i`。
var avatarURLRe = regexp.MustCompile(`(?i)^https?://`)

// bcryptMaxBytes 是 bcrypt 的密码上限。
//
// 关键差异：PHP 的 password_hash / password_verify（以及 C 版 crypt）对超过 72 字节的
// 密码**静默截断**，而 golang.org/x/crypto/bcrypt v0.31.0 的 GenerateFromPassword 与
// CompareHashAndPassword 都会返回 ErrPasswordTooLong（bcrypt.go:67 / :96）。
// 如果不截断，老用户里那些「长密码」在迁移后会突然登录失败 —— 所以这里与 PHP 对齐。
const bcryptMaxBytes = 72

func bcryptPassword(pw string) []byte {
	if len(pw) > bcryptMaxBytes {
		return []byte(pw[:bcryptMaxBytes])
	}
	return []byte(pw)
}

// verifyPassword 对齐 PHP 的 password_verify()：任何异常（含空哈希、非法 bcrypt）
// 都只是「验证失败」，绝不 panic、绝不 500。
func verifyPassword(pw, hash string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), bcryptPassword(pw)) == nil
}

// hashPassword 对齐 password_hash($pw, PASSWORD_DEFAULT)（bcrypt, cost 10）。
// Go 生成的是 `$2a$10$…`：契约 §4 已实测 PHP 的 password_verify 认这个前缀，
// 现有 59 条 `$2y$…` 也由 verifyPassword 正常校验，**不改造既有哈希**。
func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(bcryptPassword(pw), 10)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// hashEquals 对齐 PHP 的 hash_equals()（常数时间比较）。
func hashEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// randInt 对齐 random_int($min, $max)（闭区间、CSPRNG）。
func randInt(min, max int64) int64 {
	if max <= min {
		return min
	}
	n, err := crand.Int(crand.Reader, big.NewInt(max-min+1))
	if err != nil {
		return min
	}
	return min + n.Int64()
}

// pad6 对齐 str_pad((string)$n, 6, '0', STR_PAD_LEFT)。
func pad6(n int64) string { return fmt.Sprintf("%06d", n) }

// intValue 把 QueryValue / Row 取到的值按 PHP 的 (int) 语义转整数。
func intValue(v any, ok bool) int64 {
	if !ok || v == nil {
		return 0
	}
	return phpInt(v)
}

// requireUser 对齐 current_user_or_401()：未登录直接回 401 并返回 false。
func (rt *Router) requireUser(c *Ctx) (store.Row, bool) {
	me, err := rt.accounts().CurrentUserRow(c.R.Context(), c.Token())
	if err != nil {
		c.dbError(err)
		return nil, false
	}
	if me == nil {
		c.Error("登录已失效", 401)
		return nil, false
	}
	return me, true
}

// captchaCheck 对齐 api.php:3476 captcha_check()。
func (rt *Router) captchaCheck(c *Ctx, token, code string) bool {
	if token == "" || code == "" {
		return false
	}
	row, err := rt.accounts().QueryRow(c.R.Context(), "SELECT * FROM captcha_codes WHERE token = ? LIMIT 1", token)
	if err != nil {
		c.Log().Warn("captcha_check 查询失败, 按校验失败处理", "err", err)
		return false
	}
	if row == nil {
		return false
	}
	if rowInt(row, "used", 0) == 1 {
		return false
	}
	ts, ok := phpStrtotime(rowStr(row, "expires_at", ""))
	if !ok {
		ts = 0
	}
	if ts < nowUnix() {
		return false
	}
	// 「答错也作废」：只要走到了这里就无条件置 used = 1（对齐 PHP，别改）。
	if _, err := rt.accounts().Exec(c.R.Context(), "UPDATE captcha_codes SET used = 1 WHERE id = ?", rowInt(row, "id", 0)); err != nil {
		c.Log().Warn("captcha_check 置 used 失败, 按校验失败处理", "err", err)
		return false
	}
	return asciiUpper(phpTrim(code)) == asciiUpper(rowStr(row, "code", ""))
}

// notifyPush 对齐 api.php:3801 notify_push()：插入失败只记日志。
func (rt *Router) notifyPush(c *Ctx, uid int64, title, content, typ string) {
	if uid <= 0 {
		return
	}
	_, err := rt.accounts().Exec(c.R.Context(),
		"INSERT INTO notifications (user_id, title, content, type, link, is_read) VALUES (?, ?, ?, ?, ?, 0)",
		uid, title, content, typ, "")
	if err != nil {
		c.Log().Error("notify_push 失败（PHP 此处忽略异常）", "err", err)
	}
}

// userMuteState 对齐 api.php:3817 user_mute_state()：过期即删并返回「没有禁言」。
func (rt *Router) userMuteState(c *Ctx, uid, gid int64) (store.Row, bool) {
	row, err := rt.accounts().QueryRow(c.R.Context(),
		`SELECT id, group_id, until_at, reason FROM social_user_mutes
          WHERE user_id = ? AND (group_id = ? OR group_id = 0)
          ORDER BY group_id DESC LIMIT 1`, uid, gid)
	if err != nil {
		c.Log().Warn("查询禁言状态失败, 按未禁言处理", "err", err)
		return nil, false
	}
	if row == nil {
		return nil, false
	}
	until := rowStr(row, "until_at", "")
	if until != "" {
		ts, ok := phpStrtotime(until)
		if !ok {
			ts = 0 // PHP 的 strtotime 失败返回 false(=0)，0 <= time() 为真 -> 清理
		}
		if ts <= nowUnix() {
			if _, err := rt.accounts().Exec(c.R.Context(), "DELETE FROM social_user_mutes WHERE id = ?", rowInt(row, "id", 0)); err != nil {
				c.Log().Warn("清理过期禁言失败", "err", err)
			}
			return nil, false
		}
	}
	return row, true
}

// muteLeftText 对齐 api.php:4147 mute_left_text()。
func muteLeftText(m store.Row) string {
	if m == nil {
		return ""
	}
	until := rowStr(m, "until_at", "")
	if until == "" {
		return "永久"
	}
	ts, ok := phpStrtotime(until)
	if !ok {
		return ""
	}
	left := ts - nowUnix()
	if left <= 0 {
		return ""
	}
	if left >= 86400 {
		return "剩余 " + strconv.FormatInt(maxInt64(1, left/86400), 10) + " 天"
	}
	if left >= 3600 {
		return "剩余 " + strconv.FormatInt(maxInt64(1, left/3600), 10) + " 小时"
	}
	// PHP: (int)ceil($left / 60)
	return "剩余 " + strconv.FormatInt(maxInt64(1, (left+59)/60), 10) + " 分钟"
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// pmConvID 对齐 api.php:4232 pm_conv_id()。
func (rt *Router) pmConvID(c *Ctx, a, b int64, create bool) int64 {
	if a == b {
		return 0
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	v, ok, err := rt.accounts().QueryValue(c.R.Context(), "SELECT id FROM social_pms WHERE user_a = ? AND user_b = ?", lo, hi)
	if err != nil {
		c.Log().Warn("查询私聊会话失败, 按不存在处理", "err", err)
		return 0
	}
	id := intValue(v, ok)
	if id > 0 || !create {
		return id
	}
	// 并发下已存在会报错，PHP 也是忽略。
	if _, err := rt.accounts().Exec(c.R.Context(), "INSERT INTO social_pms (user_a, user_b, created_at) VALUES (?, ?, NOW())", lo, hi); err != nil {
		c.Log().Debug("创建私聊会话失败（并发下已存在）, 忽略", "err", err)
	}
	v, ok, err = rt.accounts().QueryValue(c.R.Context(), "SELECT id FROM social_pms WHERE user_a = ? AND user_b = ?", lo, hi)
	if err != nil || !ok {
		return 0
	}
	return intValue(v, ok)
}

// ---------- login ----------

// handleLogin 对齐 api.php:185。
func (rt *Router) handleLogin(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "login")
	db := rt.accounts()
	ctx := r.Context()

	username := phpTrim(c.ParamStr("username", ""))
	password := phpStr(c.paramAny("password"))
	if phpFalsy(username) || phpFalsy(password) {
		c.Error("用户名和密码不能为空", 1)
		return
	}
	// 支持用户名或邮箱登录；空字符串不算匹配 email（SQL 里就是这么写的）。
	user, err := db.QueryRow(ctx, "SELECT * FROM users WHERE username = ? OR (email <> '' AND email = ?)", username, username)
	if err != nil {
		c.dbError(err)
		return
	}
	if user == nil || !verifyPassword(password, rowStr(user, "password", "")) {
		// 两种失败同一条文案，不泄露账号是否存在。
		c.Error("用户名或密码错误", 1)
		return
	}
	if rowInt(user, "is_active", 0) != 1 {
		c.Error("账号已被封禁, 请联系管理员", 403)
		return
	}
	token := randomHex(32)
	id := rowInt(user, "id", 0)
	if _, err := db.Exec(ctx, "UPDATE users SET token = ? WHERE id = ?", token, id); err != nil {
		c.dbError(err)
		return
	}
	// 多会话：写 sessions 失败只记日志（PHP 用 try/catch 忽略）。
	if _, err := db.Exec(ctx, "INSERT INTO sessions (user_id, token) VALUES (?, ?)", id, token); err != nil {
		c.Log().Warn("登录写 sessions 失败（PHP 此处忽略异常）", "err", err, "uid", id)
	}
	if _, err := db.Exec(ctx, "UPDATE users SET last_login_at = NOW() WHERE id = ?", id); err != nil {
		c.Log().Warn("登录写 last_login_at 失败（PHP 此处忽略异常）", "err", err, "uid", id)
	}
	// 注意：返回的是登录前取到的那一行（PHP 也是），token 单独给顶层字段。
	c.JSON(phpjson.New().
		Set("token", token).
		Set("user", userPublic(user)), 0, "ok", 0)
}

// ---------- send_code ----------

// handleSendCode 对齐 api.php:209。
func (rt *Router) handleSendCode(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "send_code")
	db := rt.accounts()
	ctx := r.Context()

	email := phpTrim(c.ParamStr("email", ""))
	purpose := "register"
	if c.ParamStr("purpose", "register") == "reset" {
		purpose = "reset"
	}
	if !rt.captchaCheck(c, c.ParamStr("captcha_token", ""), c.ParamStr("captcha_code", "")) {
		c.Error("图形验证码错误或已过期", 1)
		return
	}
	if !phpValidateEmail(email) {
		c.Error("邮箱格式不正确", 1)
		return
	}
	row, err := db.QueryRow(ctx, "SELECT id FROM users WHERE email = ?", email)
	if err != nil {
		c.dbError(err)
		return
	}
	exists := row != nil
	if purpose == "register" && exists {
		c.Error("该邮箱已被注册", 1)
		return
	}
	if purpose == "reset" && !exists {
		c.Error("该邮箱尚未注册", 1)
		return
	}
	// 60 秒限制：Redis 只做同语义加速（命中必然等价于 DB 也会拒绝），未命中回落 DB。
	if rt.env.Redis.CodeSentRecently(ctx, email, purpose) {
		c.Error("发送太频繁, 请 60 秒后再试", 1)
		return
	}
	last, err := db.QueryRow(ctx, "SELECT created_at FROM email_codes WHERE email = ? AND purpose = ? ORDER BY id DESC LIMIT 1", email, purpose)
	if err != nil {
		c.dbError(err)
		return
	}
	lastVal := ""
	if last != nil {
		lastVal = rowStr(last, "created_at", "")
	}
	if !phpFalsy(lastVal) {
		if ts, ok := phpStrtotime(lastVal); ok && nowUnix()-ts < 60 {
			c.Error("发送太频繁, 请 60 秒后再试", 1)
			return
		}
	}
	n, _, err := db.QueryValue(ctx, "SELECT COUNT(*) FROM email_codes WHERE email = ? AND purpose = ? AND created_at > DATE_SUB(NOW(), INTERVAL 1 DAY)", email, purpose)
	if err != nil {
		c.dbError(err)
		return
	}
	if intValue(n, true) >= 10 {
		c.Error("今日发送次数已达上限", 1)
		return
	}
	ip := clientIP(r)
	n, _, err = db.QueryValue(ctx, "SELECT COUNT(*) FROM email_codes WHERE ip = ? AND created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR)", ip)
	if err != nil {
		c.dbError(err)
		return
	}
	if intValue(n, true) >= 20 {
		c.Error("操作过于频繁, 请稍后再试", 1)
		return
	}
	code := pad6(randInt(0, 999999))
	if _, err := db.Exec(ctx,
		"INSERT INTO email_codes (email, code, purpose, ip, expires_at) VALUES (?, ?, ?, ?, DATE_ADD(NOW(), INTERVAL 5 MINUTE))",
		email, code, purpose, ip); err != nil {
		c.dbError(err)
		return
	}
	rt.env.Redis.MarkCodeSent(ctx, email, purpose, codeSentTTL)
	if !rt.sendCodeMail(email, code, purpose) {
		c.Log().Error("[send_code] 邮件发送失败", "email", email)
		c.Error("邮件发送失败, 请稍后重试", 1)
		return
	}
	c.JSON(phpjson.New().Set("expires_in", 300), 0, "ok", 0)
}

// ---------- register ----------

// handleRegister 对齐 api.php:242。
func (rt *Router) handleRegister(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "register")
	db := rt.accounts()
	ctx := r.Context()

	username := phpTrim(c.ParamStr("username", ""))
	password := phpStr(c.paramAny("password"))
	nickname := phpTrim(c.ParamStr("nickname", ""))
	email := phpTrim(c.ParamStr("email", ""))
	code := phpTrim(c.ParamStr("code", ""))

	if !usernameRe.MatchString(username) {
		c.Error("用户名只能包含字母数字下划线(3-20位)", 1)
		return
	}
	// strlen()：字节长度，不是字符数。
	if len(password) < 6 {
		c.Error("密码至少 6 位", 1)
		return
	}
	if !phpValidateEmail(email) {
		c.Error("邮箱格式不正确", 1)
		return
	}
	if nickname == "" {
		nickname = username
	}
	if mbStrlen(nickname) > 20 {
		c.Error("昵称不能超过 20 个字", 1)
		return
	}
	row, err := db.QueryRow(ctx, "SELECT id FROM users WHERE username = ?", username)
	if err != nil {
		c.dbError(err)
		return
	}
	if row != nil {
		c.Error("用户名已存在", 1)
		return
	}
	row, err = db.QueryRow(ctx, "SELECT id FROM users WHERE email = ?", email)
	if err != nil {
		c.dbError(err)
		return
	}
	if row != nil {
		c.Error("该邮箱已被注册", 1)
		return
	}
	row, err = db.QueryRow(ctx,
		"SELECT id, code, tries FROM email_codes WHERE email = ? AND purpose = 'register' AND used = 0 AND expires_at > NOW() ORDER BY id DESC LIMIT 1", email)
	if err != nil {
		c.dbError(err)
		return
	}
	if row == nil {
		c.Error("验证码错误或已过期", 1)
		return
	}
	if rowInt(row, "tries", 0) >= 5 {
		c.Error("验证码错误次数过多, 请重新获取", 1)
		return
	}
	if !hashEquals(rowStr(row, "code", ""), code) {
		if _, err := db.Exec(ctx, "UPDATE email_codes SET tries = tries + 1 WHERE id = ?", rowInt(row, "id", 0)); err != nil {
			c.Log().Warn("验证码错误次数自增失败", "err", err)
		}
		c.Error("验证码错误或已过期", 1)
		return
	}
	if _, err := db.Exec(ctx, "UPDATE email_codes SET used = 1 WHERE id = ?", rowInt(row, "id", 0)); err != nil {
		c.Log().Warn("验证码置已用失败", "err", err)
	}
	// 邮箱为空串时写 NULL（PHP: $emailVal = $email === '' ? null : $email）。
	var emailVal any
	if email != "" {
		emailVal = email
	}
	hash, err := hashPassword(password)
	if err != nil {
		c.Error("服务器错误: "+err.Error(), 500)
		return
	}
	res, err := db.Exec(ctx,
		"INSERT INTO users (username, password, nickname, email, email_verified, avatar, role, last_login_at) VALUES (?, ?, ?, ?, 1, ?, 'user', NOW())",
		username, hash, nickname, emailVal, qqAvatarFromEmail(email))
	if err != nil {
		c.dbError(err)
		return
	}
	uid := res.LastInsertID
	token := randomHex(32)
	// 注册即登录：token 必须落库，否则返回的 token 是孤儿，后续鉴权全部 401。
	if _, err := db.Exec(ctx, "UPDATE users SET token = ? WHERE id = ?", token, uid); err != nil {
		c.dbError(err)
		return
	}
	if _, err := db.Exec(ctx, "INSERT INTO sessions (user_id, token) VALUES (?, ?)", uid, token); err != nil {
		c.Log().Warn("注册写 sessions 失败（PHP 此处忽略异常）", "err", err, "uid", uid)
	}
	newUser, err := db.QueryRow(ctx, "SELECT * FROM users WHERE id = ?", uid)
	if err != nil {
		c.dbError(err)
		return
	}
	rt.notifyPush(c, uid, "欢迎加入风铃分享库",
		"你好, "+nickname+"!\n账号已创建成功, 邮箱已验证。\n可到「我的 - 社交」参与群组聊天, 有问题欢迎在群里反馈。", "system")
	c.JSON(phpjson.New().
		Set("token", token).
		Set("user", userPublic(newUser)), 0, "ok", 0)
}

// ---------- user_me / user_update / user_password ----------

// handleUserMe 对齐 api.php:287。
func (rt *Router) handleUserMe(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "user_me")
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	c.JSON(userPublic(me), 0, "ok", 0)
}

// handleUserUpdate 对齐 api.php:290。
func (rt *Router) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "user_update")
	db := rt.accounts()
	ctx := r.Context()
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	fields := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if c.HasParam("nickname") {
		nickname := phpTrim(c.ParamStr("nickname", ""))
		if nickname == "" {
			c.Error("昵称不能为空", 1)
			return
		}
		if mbStrlen(nickname) > 20 {
			c.Error("昵称不能超过 20 个字", 1)
			return
		}
		fields = append(fields, "nickname = ?")
		args = append(args, nickname)
	}
	if c.HasParam("bio") {
		bio := phpTrim(c.ParamStr("bio", ""))
		if mbStrlen(bio) > 100 {
			c.Error("简介不能超过 100 个字", 1)
			return
		}
		fields = append(fields, "bio = ?")
		args = append(args, bio)
	}
	if c.HasParam("avatar") {
		avatar := phpTrim(c.ParamStr("avatar", ""))
		if avatar != "" && !avatarURLRe.MatchString(avatar) {
			c.Error("头像地址不合法", 1)
			return
		}
		fields = append(fields, "avatar = ?")
		args = append(args, avatar)
	}
	if len(fields) == 0 {
		c.Error("没有需要更新的内容", 1)
		return
	}
	id := rowInt(me, "id", 0)
	args = append(args, id)
	if _, err := db.Exec(ctx, "UPDATE users SET "+strings.Join(fields, ", ")+" WHERE id = ?", args...); err != nil {
		c.dbError(err)
		return
	}
	updated, err := db.QueryRow(ctx, "SELECT * FROM users WHERE id = ?", id)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(userPublic(updated), 0, "ok", 0)
}

// handleUserPassword 对齐 api.php:341。
func (rt *Router) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "user_password")
	db := rt.accounts()
	ctx := r.Context()
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	oldPw := phpStr(c.paramAny("old_password"))
	newPw := phpStr(c.paramAny("new_password"))
	if len(newPw) < 6 {
		c.Error("密码至少 6 位", 1)
		return
	}
	if !verifyPassword(oldPw, rowStr(me, "password", "")) {
		c.Error("原密码不正确", 1)
		return
	}
	hash, err := hashPassword(newPw)
	if err != nil {
		c.Error("服务器错误: "+err.Error(), 500)
		return
	}
	// PHP 只改 password，不吊销其它会话。
	if _, err := db.Exec(ctx, "UPDATE users SET password = ? WHERE id = ?", hash, rowInt(me, "id", 0)); err != nil {
		c.dbError(err)
		return
	}
	c.JSON(nil, 0, "ok", 0)
}

// ---------- captcha ----------

// handleCaptcha 对齐 api.php:493。
func (rt *Router) handleCaptcha(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "captcha")
	db := rt.accounts()
	ctx := r.Context()

	token := randomHex(16)
	code := captcha.NewCode()
	// 写库/清理失败都只记日志（PHP 用 try/catch 包住，接口照样返回图片）。
	if _, err := db.Exec(ctx,
		"INSERT INTO captcha_codes (token, code, ip, used, expires_at) VALUES (?, ?, ?, 0, DATE_ADD(NOW(), INTERVAL 180 SECOND))",
		token, code, clientIP(r)); err != nil {
		c.Log().Error("[captcha] 写入验证码失败（PHP 此处忽略异常）", "err", err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM captcha_codes WHERE expires_at < DATE_SUB(NOW(), INTERVAL 1 HOUR)"); err != nil {
		c.Log().Error("[captcha] 清理过期验证码失败（PHP 此处忽略异常）", "err", err)
	}
	image := "data:image/png;base64,"
	if png, err := captcha.ImagePNG(code); err == nil {
		image += base64.StdEncoding.EncodeToString(png)
	} else {
		c.Log().Error("[captcha] 生成图片失败", "err", err)
	}
	c.JSON(phpjson.New().
		Set("token", token).
		Set("image", image).
		Set("expires_in", 180), 0, "ok", 0)
}

// ---------- email_verify_send / email_verify ----------

// handleEmailVerifySend 对齐 api.php:498。
func (rt *Router) handleEmailVerifySend(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "email_verify_send")
	db := rt.accounts()
	ctx := r.Context()
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	if !rt.captchaCheck(c, c.ParamStr("captcha_token", ""), c.ParamStr("captcha_code", "")) {
		c.Error("图形验证码错误或已过期", 1)
		return
	}
	email := phpTrim(c.ParamStr("email", ""))
	if email == "" {
		email = phpTrim(rowStr(me, "email", ""))
	}
	if !phpValidateEmail(email) {
		c.Error("邮箱格式不正确", 1)
		return
	}
	meID := rowInt(me, "id", 0)
	row, err := db.QueryRow(ctx, "SELECT id FROM users WHERE email = ? AND id <> ?", email, meID)
	if err != nil {
		c.dbError(err)
		return
	}
	if row != nil {
		c.Error("该邮箱已被其他账号使用", 1)
		return
	}
	if rt.env.Redis.CodeSentRecently(ctx, email, "verify") {
		c.Error("发送太频繁, 请 60 秒后再试", 1)
		return
	}
	last, err := db.QueryRow(ctx, "SELECT created_at FROM email_codes WHERE email = ? AND purpose = 'verify' ORDER BY id DESC LIMIT 1", email)
	if err != nil {
		c.dbError(err)
		return
	}
	lastVal := ""
	if last != nil {
		lastVal = rowStr(last, "created_at", "")
	}
	if !phpFalsy(lastVal) {
		if ts, ok := phpStrtotime(lastVal); ok && nowUnix()-ts < 60 {
			c.Error("发送太频繁, 请 60 秒后再试", 1)
			return
		}
	}
	ip := clientIP(r)
	n, _, err := db.QueryValue(ctx, "SELECT COUNT(*) FROM email_codes WHERE ip = ? AND created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR)", ip)
	if err != nil {
		c.dbError(err)
		return
	}
	if intValue(n, true) >= 20 {
		c.Error("操作过于频繁, 请稍后再试", 1)
		return
	}
	code := pad6(randInt(0, 999999))
	if _, err := db.Exec(ctx,
		"INSERT INTO email_codes (email, code, purpose, ip, expires_at) VALUES (?, ?, ?, ?, DATE_ADD(NOW(), INTERVAL 5 MINUTE))",
		email, code, "verify", ip); err != nil {
		c.dbError(err)
		return
	}
	rt.env.Redis.MarkCodeSent(ctx, email, "verify", codeSentTTL)
	if !rt.sendCodeMail(email, code, "verify") {
		c.Log().Error("[email_verify_send] 邮件发送失败", "email", email)
		c.Error("邮件发送失败, 请稍后重试", 1)
		return
	}
	c.JSON(phpjson.New().
		Set("ok", true).
		Set("email", email).
		Set("expires_in", 300), 0, "ok", 0)
}

// handleEmailVerify 对齐 api.php:526。
func (rt *Router) handleEmailVerify(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "email_verify")
	db := rt.accounts()
	ctx := r.Context()
	me, ok := rt.requireUser(c)
	if !ok {
		return
	}
	email := phpTrim(c.ParamStr("email", ""))
	code := phpTrim(c.ParamStr("code", ""))
	if !phpValidateEmail(email) {
		c.Error("邮箱格式不正确", 1)
		return
	}
	if code == "" {
		c.Error("请输入验证码", 1)
		return
	}
	row, err := db.QueryRow(ctx,
		"SELECT id, code, tries FROM email_codes WHERE email = ? AND purpose = 'verify' AND used = 0 AND expires_at > NOW() ORDER BY id DESC LIMIT 1", email)
	if err != nil {
		c.dbError(err)
		return
	}
	if row == nil {
		c.Error("验证码错误或已过期", 1)
		return
	}
	if rowInt(row, "tries", 0) >= 5 {
		c.Error("验证码错误次数过多, 请重新获取", 1)
		return
	}
	if !hashEquals(rowStr(row, "code", ""), code) {
		if _, err := db.Exec(ctx, "UPDATE email_codes SET tries = tries + 1 WHERE id = ?", rowInt(row, "id", 0)); err != nil {
			c.Log().Warn("验证码错误次数自增失败", "err", err)
		}
		c.Error("验证码错误或已过期", 1)
		return
	}
	if _, err := db.Exec(ctx, "UPDATE email_codes SET used = 1 WHERE id = ?", rowInt(row, "id", 0)); err != nil {
		c.Log().Warn("验证码置已用失败", "err", err)
	}
	meID := rowInt(me, "id", 0)
	dup, err := db.QueryRow(ctx, "SELECT id FROM users WHERE email = ? AND id <> ?", email, meID)
	if err != nil {
		c.dbError(err)
		return
	}
	if dup != nil {
		c.Error("该邮箱已被其他账号使用", 1)
		return
	}
	if _, err := db.Exec(ctx, "UPDATE users SET email = ?, email_verified = 1 WHERE id = ?", email, meID); err != nil {
		c.dbError(err)
		return
	}
	// 验证的是 QQ 邮箱且用户还没设置过头像: 自动补上 QQ 头像。
	if qq := qqAvatarFromEmail(email); qq != "" {
		if _, err := db.Exec(ctx, "UPDATE users SET avatar = ? WHERE id = ? AND (avatar IS NULL OR avatar = '')", qq, meID); err != nil {
			c.Log().Warn("补 QQ 头像失败（PHP 此处忽略异常）", "err", err)
		}
	}
	rt.notifyPush(c, meID, "邮箱验证成功", "你的邮箱 "+email+" 已验证成功, 可用于找回密码。", "system")
	updated, err := db.QueryRow(ctx, "SELECT * FROM users WHERE id = ?", meID)
	if err != nil {
		c.dbError(err)
		return
	}
	c.JSON(phpjson.New().Set("user", userPublic(updated)), 0, "ok", 0)
}

// ---------- user_profile ----------

// handleUserProfile 对齐 api.php:559。注意：允许游客访问（$me 为 null 不报 401）。
func (rt *Router) handleUserProfile(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "user_profile")
	db := rt.accounts()
	ctx := r.Context()

	me, err := db.CurrentUserRow(ctx, c.Token())
	if err != nil {
		c.dbError(err)
		return
	}
	uid := c.ParamInt("user_id", 0)
	gidQ := c.ParamInt("group_id", 0)
	if uid <= 0 {
		c.Error("参数错误", 1)
		return
	}
	u, err := db.QueryRow(ctx,
		"SELECT id, username, nickname, avatar, bio, role, created_at, tags FROM users WHERE id = ? AND is_active = 1", uid)
	if err != nil {
		c.dbError(err)
		return
	}
	if u == nil {
		c.Error("用户不存在", 1)
		return
	}
	targetID := rowInt(u, "id", 0)
	msgCount, _, err := db.QueryValue(ctx, "SELECT COUNT(*) FROM social_messages WHERE user_id = ? AND is_recalled = 0", targetID)
	if err != nil {
		c.dbError(err)
		return
	}
	var sameGroups, convID, isMe int64
	if me != nil {
		meID := rowInt(me, "id", 0)
		if meID == targetID {
			isMe = 1
		}
		sg, _, err := db.QueryValue(ctx, `SELECT COUNT(DISTINCT m1.group_id) FROM social_messages m1
                    INNER JOIN social_messages m2 ON m1.group_id = m2.group_id
                    WHERE m1.user_id = ? AND m2.user_id = ?`, targetID, meID)
		if err != nil {
			c.dbError(err)
			return
		}
		sameGroups = intValue(sg, true)
		if isMe == 0 {
			convID = rt.pmConvID(c, meID, targetID, false)
		}
	}
	muteState, hasMute := rt.userMuteState(c, targetID, gidQ)
	var globalMute store.Row
	if gidQ == 0 {
		globalMute = muteState
	} else {
		globalMute, _ = rt.userMuteState(c, targetID, 0)
	}
	var isAdminMe int64
	if me != nil && rowStr(me, "role", "") == "admin" {
		isAdminMe = 1
	}
	var canMute int64
	if isAdminMe == 1 && isMe == 0 && rowStr(u, "role", "") != "admin" {
		canMute = 1
	}
	nickname := rowStr(u, "nickname", "")
	if nickname == "" {
		nickname = rowStr(u, "username", "")
	}
	var canChat int64
	if me != nil && isMe == 0 {
		canChat = 1
	}
	c.JSON(phpjson.New().
		Set("id", targetID).
		Set("username", rowStr(u, "username", "")).
		Set("nickname", nickname).
		Set("avatar", rowStr(u, "avatar", "")).
		Set("bio", rowStr(u, "bio", "")).
		Set("role", rowStr(u, "role", "user")).
		Set("created_at", rowStr(u, "created_at", "")).
		Set("message_count", intValue(msgCount, true)).
		Set("same_groups", sameGroups).
		Set("is_me", isMe).
		Set("can_chat", canChat).
		Set("conv_id", convID).
		Set("tags", userTagsArr(u)).
		Set("group_id", gidQ).
		Set("muted", boolToInt(hasMute)).
		Set("mute_left", muteLeftTextOf(muteState)).
		Set("mute_reason", muteReasonOf(muteState)).
		Set("global_muted", boolToInt(globalMute != nil)).
		Set("global_mute_left", muteLeftTextOf(globalMute)).
		Set("global_mute_reason", muteReasonOf(globalMute)).
		Set("is_admin_me", isAdminMe).
		Set("can_mute", canMute), 0, "ok", 0)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func muteLeftTextOf(m store.Row) string {
	if m == nil {
		return ""
	}
	return muteLeftText(m)
}

func muteReasonOf(m store.Row) string {
	if m == nil {
		return ""
	}
	return rowStr(m, "reason", "")
}

// ---------- logout ----------

// handleLogout 对齐 api.php:2411。
//
// 注意：PHP 这里用的是 param('token')，**不读 Authorization 头**；
// 所以只带 Bearer 头不带 token 参数的登出请求不会删任何会话（PHP 也是这样）。
func (rt *Router) handleLogout(w http.ResponseWriter, r *http.Request) {
	c := rt.env.newCtx(w, r, "logout")
	db := rt.accounts()
	ctx := r.Context()

	token := phpStr(c.paramAny("token"))
	if !phpFalsy(token) {
		if _, err := db.Exec(ctx, "DELETE FROM sessions WHERE token = ?", token); err != nil {
			c.dbError(err)
			return
		}
		// 仅当 users.token 等于当前 token 才清（避免顶掉其他会话）。
		if _, err := db.Exec(ctx, "UPDATE users SET token = NULL WHERE token = ?", token); err != nil {
			c.dbError(err)
			return
		}
	}
	c.JSON(nil, 0, "ok", 0)
}
