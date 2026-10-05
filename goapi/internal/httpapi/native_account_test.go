package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 2 账号 action 的单测：全部走「脚本化假数据访问层」，不连真实 MySQL/Redis。
//
// 覆盖重点（派单书要求）：
//   - 每个 action 的参数校验分支与逐字中文文案；
//   - JSON code / HTTP 状态码映射（403 走 HTTP 400、401 走 HTTP 401）；
//   - token 解析优先级（Authorization: Bearer → ?token=）；
//   - is_active != 1 视为未登录（401 登录已失效）；
//   - JSON 字段序与 `/` 转义的 golden 断言；
//   - captcha_check「答错也作废」；
//   - bcrypt `$2y$` 双向兼容（含 72 字节截断）。

// ---------- 假数据访问层 ----------

type rowRule struct {
	match string
	row   store.Row
	err   error
}

type valRule struct {
	match string
	val   any
	ok    bool
	err   error
}

type fakeCall struct {
	Method string
	Query  string
	Args   []any
}

type fakeAccounts struct {
	rows     []rowRule
	vals     []valRule
	me       store.Row
	meErr    error
	insertID int64
	execErr  error
	calls    []fakeCall
}

func (f *fakeAccounts) CurrentUserRow(_ context.Context, token string) (store.Row, error) {
	f.calls = append(f.calls, fakeCall{Method: "CurrentUserRow", Args: []any{token}})
	if f.meErr != nil {
		return nil, f.meErr
	}
	if token == "" {
		return nil, nil
	}
	return f.me, nil
}

func (f *fakeAccounts) QueryRow(_ context.Context, query string, args ...any) (store.Row, error) {
	f.calls = append(f.calls, fakeCall{Method: "QueryRow", Query: query, Args: args})
	for _, r := range f.rows {
		if strings.Contains(query, r.match) {
			return r.row, r.err
		}
	}
	return nil, nil
}

func (f *fakeAccounts) QueryAll(_ context.Context, query string, args ...any) ([]store.Row, error) {
	f.calls = append(f.calls, fakeCall{Method: "QueryAll", Query: query, Args: args})
	return nil, nil
}

func (f *fakeAccounts) QueryValue(_ context.Context, query string, args ...any) (any, bool, error) {
	f.calls = append(f.calls, fakeCall{Method: "QueryValue", Query: query, Args: args})
	for _, r := range f.vals {
		if strings.Contains(query, r.match) {
			return r.val, r.ok, r.err
		}
	}
	return nil, false, nil
}

func (f *fakeAccounts) Exec(_ context.Context, query string, args ...any) (store.ExecResult, error) {
	f.calls = append(f.calls, fakeCall{Method: "Exec", Query: query, Args: args})
	if f.execErr != nil {
		return store.ExecResult{}, f.execErr
	}
	return store.ExecResult{LastInsertID: f.insertID, RowsAffected: 1}, nil
}

// execs 返回所有匹配子串的写操作，用来断言「PHP 会执行的写语句 Go 也执行了」。
func (f *fakeAccounts) execs(match string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == "Exec" && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAccounts) queries(match string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method != "Exec" && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

type fakeMailer struct {
	ok    bool
	calls []string
}

func (m *fakeMailer) SendCode(to, code, purpose string) bool {
	m.calls = append(m.calls, purpose+":"+to+":"+code)
	return m.ok
}

// ---------- 测试脚手架 ----------

func newAccountRouter(db accountsDB, m codeMailer) *Router {
	return New(Env{Cfg: config.Load(), Log: testLogger(), Accounts: db, Mailer: m})
}

func callAction(rt *Router, method, target, body, contentType string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, req)
	return w
}

func callJSON(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json", nil)
}

// tokenRe 从响应里把随机 token 抠出来（响应里其它地方不会出现 64 位十六进制）。
var tokenRe = regexp.MustCompile(`"token":"([0-9a-f]{64})"`)

func maskToken(t *testing.T, body string) string {
	t.Helper()
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("响应里没有 64 位小写十六进制 token: %s", body)
	}
	return strings.Replace(body, m[1], "<TOKEN>", 1)
}

// bcryptTestHash 是我用 Go 的 bcrypt 生成后把前缀换成 `$2y$` 的固定哈希
// （对应密码 fengling-test-pw）。`$2y$` 是库里 59 个用户的实际前缀。
const bcryptTestHash = "$2y$10$e0iqqkBdopJQhPJPIawv9.Odr3uA9ddIiJCIwWyUnj8Ou4MVJVJsa"

// phpManualHash 是 PHP 手册里 password_hash('rasmuslerdorf') 的真实 `$2y$` 输出，
// 用来证明「PHP 生成的哈希 Go 能校验」。
const phpManualHash = "$2y$10$.vGA1O9wmRjrwAVXD98HNOgsNpDczlqm3Jq7KnEd1rVAGv3Fykk1a"

func testUserRow() store.Row {
	return store.Row{
		"id":             int64(7),
		"username":       "alice",
		"nickname":       "爱丽丝",
		"email":          "a@b.com",
		"email_verified": int64(1),
		"avatar":         "https://q1.qlogo.cn/g?b=qq&nk=12345&s=640",
		"bio":            "hi",
		"role":           "user",
		"is_active":      int64(1),
		"created_at":     "2026-01-02 03:04:05",
		"tags":           "vip,老用户",
		"password":       bcryptTestHash,
	}
}

// ---------- bcrypt 兼容 ----------

func TestBcryptVerifiesPHPGenerated2yHash(t *testing.T) {
	if !verifyPassword("rasmuslerdorf", phpManualHash) {
		t.Fatal("PHP 手册里 password_hash 生成的 $2y$ 哈希必须能校验通过")
	}
	if verifyPassword("wrong", phpManualHash) {
		t.Fatal("错误密码必须校验失败")
	}
	if !verifyPassword("fengling-test-pw", bcryptTestHash) {
		t.Fatal("$2y$ 前缀的自造哈希必须能校验通过")
	}
}

// Go 的 bcrypt 对 >72 字节密码会报 ErrPasswordTooLong，而 PHP 是静默截断。
// 不处理这个差异，老用户里的长密码迁移后会登不上。
func TestBcryptTruncatesAt72BytesLikePHP(t *testing.T) {
	long := strings.Repeat("a", 80)
	hash, err := hashPassword(long)
	if err != nil {
		t.Fatalf("生成哈希不该失败: %v", err)
	}
	if !verifyPassword(long, hash) {
		t.Fatal("同一个长密码必须能校验通过")
	}
	if !verifyPassword(long[:72], hash) {
		t.Fatal("bcrypt 语义是只取前 72 字节, 前 72 字节必须能校验通过")
	}
	if verifyPassword(long[:71], hash) {
		t.Fatal("前 71 字节不该校验通过")
	}
}

// ---------- login ----------

func TestLoginSuccessGolden(t *testing.T) {
	db := &fakeAccounts{rows: []rowRule{{match: "FROM users WHERE username = ?", row: testUserRow()}}}
	rt := newAccountRouter(db, &fakeMailer{ok: true})

	w := callJSON(t, rt, "login", `{"username":" alice ","password":"fengling-test-pw"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	assertCORS(t, w.Header())
	want := `{"code":0,"msg":"ok","data":{"token":"<TOKEN>","user":{` +
		`"id":7,"username":"alice","nickname":"爱丽丝","email":"a@b.com","email_verified":1,` +
		`"avatar":"https:\/\/q1.qlogo.cn\/g?b=qq&nk=12345&s=640","bio":"hi","role":"user",` +
		`"is_active":1,"created_at":"2026-01-02 03:04:05","tags":["vip","老用户"]}}}`
	if got := maskToken(t, w.Body.String()); got != want {
		t.Fatalf("字段序/转义与 PHP 不一致:\n got=%s\nwant=%s", got, want)
	}
	// username 被 trim（PHP trim(param('username',''))）；密码不做 trim。
	if q := db.queries("FROM users WHERE username = ?"); len(q) != 1 || q[0].Args[0] != "alice" {
		t.Fatalf("登录查询参数不对: %#v", q)
	}
	if len(db.execs("UPDATE users SET token = ?")) != 1 {
		t.Fatal("必须写 users.token")
	}
	if len(db.execs("INSERT INTO sessions")) != 1 {
		t.Fatal("必须写 sessions")
	}
	if len(db.execs("UPDATE users SET last_login_at = NOW()")) != 1 {
		t.Fatal("必须写 last_login_at")
	}
}

func TestLoginEmptyCredentials(t *testing.T) {
	cases := []string{
		`{}`,
		`{"username":"a"}`,
		`{"username":"a","password":""}`,
		`{"username":"","password":"x"}`,
		`{"username":"a","password":0}`,
		`{"username":"a","password":"0"}`, // PHP 的 !'0' 为真
	}
	for _, body := range cases {
		db := &fakeAccounts{}
		w := callJSON(t, newAccountRouter(db, &fakeMailer{}), "login", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d", body, w.Code)
		}
		if got, want := w.Body.String(), `{"code":1,"msg":"用户名和密码不能为空","data":null}`; got != want {
			t.Errorf("%s: got=%s", body, got)
		}
		if len(db.calls) != 0 {
			t.Errorf("%s: 参数不合法时不该查库", body)
		}
	}
}

func TestLoginWrongPasswordOrUnknownUserSameMessage(t *testing.T) {
	// 未知用户
	w1 := callJSON(t, newAccountRouter(&fakeAccounts{}, &fakeMailer{}), "login", `{"username":"nobody","password":"x"}`)
	// 密码错
	u := testUserRow()
	db2 := &fakeAccounts{rows: []rowRule{{match: "FROM users WHERE username = ?", row: u}}}
	w2 := callJSON(t, newAccountRouter(db2, &fakeMailer{}), "login", `{"username":"alice","password":"bad"}`)
	want := `{"code":1,"msg":"用户名或密码错误","data":null}`
	for i, w := range []*httptest.ResponseRecorder{w1, w2} {
		if w.Code != http.StatusBadRequest {
			t.Fatalf("case%d status=%d", i, w.Code)
		}
		if w.Body.String() != want {
			t.Fatalf("case%d got=%s", i, w.Body.String())
		}
	}
}

// 封禁用户：JSON code 是 403，但 HTTP 状态码是 400（config.php json_out 的怪癖）。
func TestLoginBannedUserIsJSON403HTTP400(t *testing.T) {
	u := testUserRow()
	u["is_active"] = int64(0)
	db := &fakeAccounts{rows: []rowRule{{match: "FROM users WHERE username = ?", row: u}}}
	w := callJSON(t, newAccountRouter(db, &fakeMailer{}), "login", `{"username":"alice","password":"fengling-test-pw"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP 状态码必须是 400, 实际 %d", w.Code)
	}
	if got, want := w.Body.String(), `{"code":403,"msg":"账号已被封禁, 请联系管理员","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	if len(db.execs("UPDATE users SET token")) != 0 {
		t.Fatal("封禁用户不该写 token")
	}
}

// is_active != 1 的用户即使 token 有效，current_user() 也查不到 -> 401「登录已失效」。
func TestInactiveUserTreatedAsNotLoggedIn(t *testing.T) {
	for _, action := range []string{"user_me", "user_update", "user_password", "email_verify_send", "email_verify"} {
		db := &fakeAccounts{me: nil} // CurrentUserRow 返回 nil = 第一条 JOIN 带 is_active=1 查不到
		rt := newAccountRouter(db, &fakeMailer{})
		w := callJSON(t, rt, action, `{"token":"deadbeef"}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: HTTP 必须是 401, 实际 %d body=%s", action, w.Code, w.Body.String())
		}
		if got, want := w.Body.String(), `{"code":401,"msg":"登录已失效","data":null}`; got != want {
			t.Fatalf("%s: got=%s", action, got)
		}
	}
}

// ---------- token 解析优先级 ----------

func TestTokenPriorityBearerThenQueryForAccountActions(t *testing.T) {
	// Bearer 优先
	db := &fakeAccounts{me: testUserRow()}
	rt := newAccountRouter(db, &fakeMailer{})
	w := callAction(rt, "GET", "/api.php?action=user_me&token=fromquery", "", "",
		map[string]string{"Authorization": "bearer   fromheader"})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := db.calls[0].Args[0]; got != "fromheader" {
		t.Fatalf("Bearer 应优先, 实际用了 %#v", got)
	}
	// 没有 Bearer 时回落 ?token=
	db2 := &fakeAccounts{me: testUserRow()}
	rt2 := newAccountRouter(db2, &fakeMailer{})
	callAction(rt2, "GET", "/api.php?action=user_me&token=fromquery", "", "", nil)
	if got := db2.calls[0].Args[0]; got != "fromquery" {
		t.Fatalf("应回落查询串 token, 实际 %#v", got)
	}
	// 既没有 Bearer 也没有 token -> 未登录
	db3 := &fakeAccounts{me: testUserRow()}
	rt3 := newAccountRouter(db3, &fakeMailer{})
	w3 := callAction(rt3, "GET", "/api.php?action=user_me", "", "", nil)
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("空 token 必须 401, 实际 %d", w3.Code)
	}
}

func TestUserMeGolden(t *testing.T) {
	rt := newAccountRouter(&fakeAccounts{me: testUserRow()}, &fakeMailer{})
	w := callAction(rt, "GET", "/api.php?action=user_me&token=t", "", "", nil)
	want := `{"code":0,"msg":"ok","data":{` +
		`"id":7,"username":"alice","nickname":"爱丽丝","email":"a@b.com","email_verified":1,` +
		`"avatar":"https:\/\/q1.qlogo.cn\/g?b=qq&nk=12345&s=640","bio":"hi","role":"user",` +
		`"is_active":1,"created_at":"2026-01-02 03:04:05","tags":["vip","老用户"]}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("got=%s\nwant=%s", got, want)
	}
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatal("user_public 绝不能输出 password/token")
	}
}

// ---------- user_update / user_password ----------

func TestUserUpdateFieldOrderAndTexts(t *testing.T) {
	base := func() *fakeAccounts {
		return &fakeAccounts{
			me:   testUserRow(),
			rows: []rowRule{{match: "SELECT * FROM users WHERE id = ?", row: testUserRow()}},
		}
	}
	// 三个字段都提交 -> SQL 里的字段顺序必须是 nickname, bio, avatar
	db := base()
	w := callJSON(t, newAccountRouter(db, &fakeMailer{}),
		"user_update", `{"token":"t","nickname":" 新昵称 ","bio":"简介","avatar":"https://x.cn/a.png"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	upd := db.execs("UPDATE users SET")
	if len(upd) != 1 {
		t.Fatalf("应有 1 条 UPDATE, 实际 %d", len(upd))
	}
	if !strings.Contains(upd[0].Query, "nickname = ?, bio = ?, avatar = ?") {
		t.Fatalf("字段顺序不对: %s", upd[0].Query)
	}
	if len(upd[0].Args) != 4 || upd[0].Args[0] != "新昵称" || upd[0].Args[1] != "简介" || upd[0].Args[2] != "https://x.cn/a.png" || upd[0].Args[3] != int64(7) {
		t.Fatalf("参数不对: %#v", upd[0].Args)
	}

	cases := []struct {
		body string
		want string
	}{
		{`{"token":"t","nickname":"  "}`, "昵称不能为空"},
		{`{"token":"t","nickname":"` + strings.Repeat("字", 21) + `"}`, "昵称不能超过 20 个字"},
		{`{"token":"t","bio":"` + strings.Repeat("字", 101) + `"}`, "简介不能超过 100 个字"},
		{`{"token":"t","avatar":"ftp://x"}`, "头像地址不合法"},
		{`{"token":"t"}`, "没有需要更新的内容"},
	}
	for _, c := range cases {
		db := base()
		w := callJSON(t, newAccountRouter(db, &fakeMailer{}), "user_update", c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d", c.body, w.Code)
		}
		if !strings.Contains(w.Body.String(), `"msg":"`+c.want+`"`) || !strings.Contains(w.Body.String(), `"code":1`) {
			t.Errorf("%s: got=%s", c.body, w.Body.String())
		}
		if len(db.execs("UPDATE users SET")) != 0 {
			t.Errorf("%s: 校验失败不该写库", c.body)
		}
	}
}

func TestUserUpdateHasParamTreatsJSONNullAsSubmitted(t *testing.T) {
	// PHP 的 has_param 用 array_key_exists，显式 null 也算提交过 -> 昵称为空报错。
	db := &fakeAccounts{me: testUserRow()}
	w := callJSON(t, newAccountRouter(db, &fakeMailer{}), "user_update", `{"token":"t","nickname":null}`)
	if want := `{"code":1,"msg":"昵称不能为空","data":null}`; w.Body.String() != want {
		t.Fatalf("got=%s", w.Body.String())
	}
}

func TestUserPasswordTexts(t *testing.T) {
	db := &fakeAccounts{me: testUserRow()}
	rt := newAccountRouter(db, &fakeMailer{})
	w := callJSON(t, rt, "user_password", `{"token":"t","old_password":"fengling-test-pw","new_password":"12345"}`)
	if want := `{"code":1,"msg":"密码至少 6 位","data":null}`; w.Body.String() != want {
		t.Fatalf("got=%s", w.Body.String())
	}
	w = callJSON(t, rt, "user_password", `{"token":"t","old_password":"bad","new_password":"123456"}`)
	if want := `{"code":1,"msg":"原密码不正确","data":null}`; w.Body.String() != want {
		t.Fatalf("got=%s", w.Body.String())
	}
	db2 := &fakeAccounts{me: testUserRow()}
	w = callJSON(t, newAccountRouter(db2, &fakeMailer{}), "user_password",
		`{"token":"t","old_password":"fengling-test-pw","new_password":"123456"}`)
	if w.Code != http.StatusOK || w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	upd := db2.execs("UPDATE users SET password = ?")
	if len(upd) != 1 || upd[0].Args[1] != int64(7) {
		t.Fatalf("更新密码语句不对: %#v", upd)
	}
	if !verifyPassword("123456", upd[0].Args[0].(string)) {
		t.Fatal("写进库的新哈希必须能校验新密码")
	}
}

// ---------- captcha ----------

func TestCaptchaCheckMarksUsedEvenWhenWrong(t *testing.T) {
	row := store.Row{"id": int64(3), "code": "AB34", "used": int64(0), "expires_at": "2099-01-01 00:00:00"}
	db := &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?", row: row}}}
	rt := newAccountRouter(db, &fakeMailer{})
	w := callJSON(t, rt, "send_code", `{"captcha_token":"tk","captcha_code":"ZZZZ","email":"x@y.com"}`)
	if want := `{"code":1,"msg":"图形验证码错误或已过期","data":null}`; w.Body.String() != want {
		t.Fatalf("got=%s", w.Body.String())
	}
	if len(db.execs("UPDATE captcha_codes SET used = 1")) != 1 {
		t.Fatal("答错也必须把 used 置 1（PHP 的「一次性」语义）")
	}
	// 大小写不敏感
	db2 := &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?", row: row}}}
	w2 := callJSON(t, newAccountRouter(db2, &fakeMailer{ok: true}), "send_code",
		`{"captcha_token":"tk","captcha_code":" ab34 ","email":"x@y.com"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("大小写/空白应被忽略, status=%d body=%s", w2.Code, w2.Body.String())
	}
}

func TestCaptchaCheckRejectsUsedMissingAndExpired(t *testing.T) {
	rt := newAccountRouter(&fakeAccounts{}, &fakeMailer{})
	if rt.captchaCheck(testCtx(t), "tk", "") {
		t.Fatal("code 为空必须失败")
	}
	if rt.captchaCheck(testCtx(t), "", "AB34") {
		t.Fatal("token 为空必须失败")
	}
	// 不存在的 token
	if rt.captchaCheck(testCtx(t), "nope", "AB34") {
		t.Fatal("查不到必须失败")
	}
	// used = 1
	used := store.Row{"id": int64(3), "code": "AB34", "used": int64(1), "expires_at": "2099-01-01 00:00:00"}
	db := &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?", row: used}}}
	if newAccountRouter(db, &fakeMailer{}).captchaCheck(testCtx(t), "tk", "AB34") {
		t.Fatal("used=1 必须失败")
	}
	if len(db.execs("UPDATE captcha_codes SET used = 1")) != 0 {
		t.Fatal("used=1 时不该再写")
	}
	// 已过期
	exp := store.Row{"id": int64(3), "code": "AB34", "used": int64(0), "expires_at": "2000-01-01 00:00:00"}
	db2 := &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?", row: exp}}}
	if newAccountRouter(db2, &fakeMailer{}).captchaCheck(testCtx(t), "tk", "AB34") {
		t.Fatal("过期必须失败")
	}
}

// testCtx 造一个只用于直接调 captchaCheck 的上下文。
func testCtx(t *testing.T) *Ctx {
	t.Helper()
	req := httptest.NewRequest("POST", "/api.php?action=send_code", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	env := &Env{Log: testLogger()}
	return env.newCtx(w, req, "send_code")
}

func TestCaptchaActionGolden(t *testing.T) {
	db := &fakeAccounts{}
	w := callAction(newAccountRouter(db, &fakeMailer{}), "GET", "/api.php?action=captcha", "", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, `{"code":0,"msg":"ok","data":{"token":"`) {
		t.Fatalf("body=%s", body)
	}
	// captcha 的 token 是 bin2hex(random_bytes(16)) = 32 位小写十六进制。
	captureToken := regexp.MustCompile(`^\{"code":0,"msg":"ok","data":\{"token":"([0-9a-f]{32})"`)
	if m := captureToken.FindStringSubmatch(body); m == nil {
		t.Fatalf("token 不是 32 位十六进制: %s", body)
	}
	// image 是 data URL，`/` 必须被转义成 `\/`（PHP json_encode 的默认行为）。
	if !strings.Contains(body, `"image":"data:image\/png;base64,iVBORw0KGgo`) {
		t.Fatalf("image 字段不对: %s", body)
	}
	if !strings.HasSuffix(body, `,"expires_in":180}}`) {
		t.Fatalf("尾部不对: %s", body)
	}
	if len(db.execs("INSERT INTO captcha_codes")) != 1 {
		t.Fatal("必须写 captcha_codes")
	}
	if len(db.execs("DELETE FROM captcha_codes")) != 1 {
		t.Fatal("必须清理过期验证码")
	}
}

// ---------- send_code ----------

func TestSendCodeValidationOrderAndTexts(t *testing.T) {
	goodCaptcha := func() *fakeAccounts {
		return &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?",
			row: store.Row{"id": int64(1), "code": "AB34", "used": int64(0), "expires_at": "2099-01-01 00:00:00"}}}}
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{"验证码错", `{"captcha_code":"ZZZZ","email":"x@y.com"}`, "图形验证码错误或已过期"},
		{"邮箱非法", `{"captcha_token":"tk","captcha_code":"AB34","email":"not-an-email"}`, "邮箱格式不正确"},
	}
	for _, c := range cases {
		w := callJSON(t, newAccountRouter(goodCaptcha(), &fakeMailer{ok: true}), "send_code", c.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d", c.name, w.Code)
		}
		if got, want := w.Body.String(), `{"code":1,"msg":"`+c.want+`","data":null}`; got != want {
			t.Fatalf("%s: got=%s", c.name, got)
		}
	}

	// register 且邮箱已存在
	db := goodCaptcha()
	db.rows = append(db.rows, rowRule{match: "FROM users WHERE email = ?", row: store.Row{"id": int64(5)}})
	w := callJSON(t, newAccountRouter(db, &fakeMailer{ok: true}), "send_code",
		`{"captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"该邮箱已被注册","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}

	// reset 且邮箱不存在
	db2 := goodCaptcha()
	w = callJSON(t, newAccountRouter(db2, &fakeMailer{ok: true}), "send_code",
		`{"captcha_token":"tk","captcha_code":"AB34","purpose":"reset","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"该邮箱尚未注册","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
}

func TestSendCodeThreeRateLimits(t *testing.T) {
	base := func() *fakeAccounts {
		return &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?",
			row: store.Row{"id": int64(1), "code": "AB34", "used": int64(0), "expires_at": "2099-01-01 00:00:00"}}}}
	}
	// 60 秒内
	db := base()
	db.rows = append(db.rows, rowRule{match: "SELECT created_at FROM email_codes",
		row: store.Row{"created_at": phpjsonTimeAgo(10)}})
	w := callJSON(t, newAccountRouter(db, &fakeMailer{ok: true}), "send_code",
		`{"captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"发送太频繁, 请 60 秒后再试","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}

	// 24 小时 >= 10
	db2 := base()
	db2.rows = append(db2.rows, rowRule{match: "SELECT created_at FROM email_codes",
		row: store.Row{"created_at": phpjsonTimeAgo(600)}})
	db2.vals = append(db2.vals, valRule{match: "INTERVAL 1 DAY", val: int64(10), ok: true})
	w = callJSON(t, newAccountRouter(db2, &fakeMailer{ok: true}), "send_code",
		`{"captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"今日发送次数已达上限","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}

	// 同 IP 1 小时 >= 20
	db3 := base()
	db3.vals = append(db3.vals, valRule{match: "WHERE ip = ?", val: int64(20), ok: true})
	w = callJSON(t, newAccountRouter(db3, &fakeMailer{ok: true}), "send_code",
		`{"captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"操作过于频繁, 请稍后再试","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	if len(db3.execs("INSERT INTO email_codes")) != 0 {
		t.Fatal("限流命中时不该写 email_codes")
	}
}

func TestSendCodeSuccessAndMailFailure(t *testing.T) {
	base := func(m codeMailer) (*fakeAccounts, *Router) {
		db := &fakeAccounts{rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?",
			row: store.Row{"id": int64(1), "code": "AB34", "used": int64(0), "expires_at": "2099-01-01 00:00:00"}}}}
		return db, newAccountRouter(db, m)
	}
	db, rt := base(&fakeMailer{ok: true})
	w := callAction(rt, "POST", "/api.php?action=send_code&captcha_token=tk&captcha_code=AB34",
		`{"email":"x@y.com"}`, "application/json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), `{"code":0,"msg":"ok","data":{"expires_in":300}}`; got != want {
		t.Fatalf("got=%s", got)
	}
	ins := db.execs("INSERT INTO email_codes")
	if len(ins) != 1 {
		t.Fatal("必须写 email_codes")
	}
	code, _ := ins[0].Args[1].(string)
	if len(code) != 6 {
		t.Fatalf("验证码必须是 6 位（含前导零）: %q", code)
	}
	for i := 0; i < 6; i++ {
		if code[i] < '0' || code[i] > '9' {
			t.Fatalf("验证码必须是数字: %q", code)
		}
	}
	if ins[0].Args[2] != "register" {
		t.Fatalf("purpose 默认必须是 register: %#v", ins[0].Args)
	}

	// 发信失败：文案与 PHP 一致，且验证码行已经入库（PHP 也是先入库再发信）。
	db2, rt2 := base(&fakeMailer{ok: false})
	w2 := callJSON(t, rt2, "send_code", `{"captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w2.Body.String(), `{"code":1,"msg":"邮件发送失败, 请稍后重试","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	if len(db2.execs("INSERT INTO email_codes")) != 1 {
		t.Fatal("发信失败前应先写入 email_codes（与 PHP 顺序一致）")
	}
}

// ---------- register ----------

func TestRegisterValidationTexts(t *testing.T) {
	validCode := rowRule{match: "purpose = 'register' AND used = 0",
		row: store.Row{"id": int64(11), "code": "123456", "tries": int64(0)}}
	base := func(rules ...rowRule) (*fakeAccounts, *Router) {
		db := &fakeAccounts{insertID: 99, rows: rules}
		return db, newAccountRouter(db, &fakeMailer{})
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{"用户名非法", `{"username":"ab","password":"123456","email":"a@b.co","code":"123456"}`, "用户名只能包含字母数字下划线(3-20位)"},
		{"用户名带中文", `{"username":"张三","password":"123456","email":"a@b.co","code":"123456"}`, "用户名只能包含字母数字下划线(3-20位)"},
		{"密码太短", `{"username":"abc","password":"12345","email":"a@b.co","code":"123456"}`, "密码至少 6 位"},
		{"邮箱非法", `{"username":"abc","password":"123456","email":"bad","code":"123456"}`, "邮箱格式不正确"},
		{"昵称太长", `{"username":"abc","password":"123456","email":"a@b.co","nickname":"` + strings.Repeat("字", 21) + `","code":"123456"}`, "昵称不能超过 20 个字"},
	}
	for _, c := range cases {
		_, rt := base(validCode)
		w := callJSON(t, rt, "register", c.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d", c.name, w.Code)
		}
		if got, want := w.Body.String(), `{"code":1,"msg":"`+c.want+`","data":null}`; got != want {
			t.Fatalf("%s: got=%s", c.name, got)
		}
	}

	// 用户名已存在（在昵称校验之后）
	db, rt := base(rowRule{match: "FROM users WHERE username = ?", row: store.Row{"id": int64(1)}}, validCode)
	w := callJSON(t, rt, "register", `{"username":"abc","password":"123456","email":"a@b.co","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"用户名已存在","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	if len(db.execs("INSERT INTO users")) != 0 {
		t.Fatal("用户名重复不该插入")
	}

	// 邮箱已注册
	_, rt = base(rowRule{match: "FROM users WHERE email = ?", row: store.Row{"id": int64(2)}}, validCode)
	w = callJSON(t, rt, "register", `{"username":"abc","password":"123456","email":"a@b.co","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"该邮箱已被注册","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}

	// 没有可用验证码
	_, rt = base()
	w = callJSON(t, rt, "register", `{"username":"abc","password":"123456","email":"a@b.co","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"验证码错误或已过期","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}

	// 错误次数过多
	_, rt = base(rowRule{match: "purpose = 'register' AND used = 0",
		row: store.Row{"id": int64(11), "code": "123456", "tries": int64(5)}})
	w = callJSON(t, rt, "register", `{"username":"abc","password":"123456","email":"a@b.co","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"验证码错误次数过多, 请重新获取","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}

	// 验证码不匹配：tries + 1
	db2, rt := base(validCode)
	w = callJSON(t, rt, "register", `{"username":"abc","password":"123456","email":"a@b.co","code":"000000"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"验证码错误或已过期","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	if len(db2.execs("UPDATE email_codes SET tries = tries + 1")) != 1 {
		t.Fatal("不匹配必须 tries + 1")
	}
	if len(db2.execs("INSERT INTO users")) != 0 {
		t.Fatal("验证码错误不该注册")
	}
}

func TestRegisterSuccessGolden(t *testing.T) {
	newRow := testUserRow()
	newRow["email"] = "new@b.com"
	newRow["nickname"] = "新用户"
	newRow["tags"] = ""
	db := &fakeAccounts{
		insertID: 99,
		rows: []rowRule{
			{match: "purpose = 'register' AND used = 0", row: store.Row{"id": int64(11), "code": "123456", "tries": int64(0)}},
			{match: "SELECT * FROM users WHERE id = ?", row: newRow},
		},
	}
	rt := newAccountRouter(db, &fakeMailer{})
	w := callJSON(t, rt, "register", `{"username":"newbie","password":"123456","email":"new@b.com","code":"123456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	want := `{"code":0,"msg":"ok","data":{"token":"<TOKEN>","user":{` +
		`"id":7,"username":"alice","nickname":"新用户","email":"new@b.com","email_verified":1,` +
		`"avatar":"https:\/\/q1.qlogo.cn\/g?b=qq&nk=12345&s=640","bio":"hi","role":"user",` +
		`"is_active":1,"created_at":"2026-01-02 03:04:05","tags":[]}}}`
	if got := maskToken(t, w.Body.String()); got != want {
		t.Fatalf("got=%s\nwant=%s", got, want)
	}
	ins := db.execs("INSERT INTO users")
	if len(ins) != 1 {
		t.Fatal("必须插入 users")
	}
	if !strings.Contains(ins[0].Query, "email_verified, avatar, role, last_login_at) VALUES (?, ?, ?, ?, 1, ?, 'user', NOW())") {
		t.Fatalf("INSERT 语句不对: %s", ins[0].Query)
	}
	if len(ins[0].Args) != 5 {
		t.Fatalf("参数个数不对: %#v", ins[0].Args)
	}
	if !verifyPassword("123456", ins[0].Args[1].(string)) {
		t.Fatal("写入的密码哈希必须能校验原密码")
	}
	if len(db.execs("UPDATE users SET token = ?")) != 1 || len(db.execs("INSERT INTO sessions")) != 1 {
		t.Fatal("注册即登录必须落 users.token 与 sessions")
	}
	if len(db.execs("INSERT INTO notifications")) != 1 {
		t.Fatal("必须推欢迎通知")
	}
	if len(db.execs("UPDATE email_codes SET used = 1")) != 1 {
		t.Fatal("验证码必须置已用")
	}
}

// 昵称为空时用用户名（PHP: if ($nickname === ”) $nickname = $username;）。
func TestRegisterNicknameDefaultsToUsername(t *testing.T) {
	db := &fakeAccounts{
		insertID: 1,
		rows: []rowRule{
			{match: "purpose = 'register' AND used = 0", row: store.Row{"id": int64(11), "code": "123456", "tries": int64(0)}},
			{match: "SELECT * FROM users WHERE id = ?", row: testUserRow()},
		},
	}
	callJSON(t, newAccountRouter(db, &fakeMailer{}), "register",
		`{"username":"newbie","password":"123456","email":"new@b.com","code":"123456"}`)
	if got := db.execs("INSERT INTO users")[0].Args[2]; got != "newbie" {
		t.Fatalf("昵称应回落用户名, 实际 %#v", got)
	}
}

// QQ 邮箱自动补头像。
func TestRegisterQqAvatar(t *testing.T) {
	db := &fakeAccounts{
		insertID: 1,
		rows: []rowRule{
			{match: "purpose = 'register' AND used = 0", row: store.Row{"id": int64(11), "code": "123456", "tries": int64(0)}},
			{match: "SELECT * FROM users WHERE id = ?", row: testUserRow()},
		},
	}
	callJSON(t, newAccountRouter(db, &fakeMailer{}), "register",
		`{"username":"newbie","password":"123456","email":"12345678@qq.com","code":"123456"}`)
	if got := db.execs("INSERT INTO users")[0].Args[4]; got != "https://q1.qlogo.cn/g?b=qq&nk=12345678&s=640" {
		t.Fatalf("QQ 头像不对: %#v", got)
	}
}

// ---------- email_verify_send / email_verify ----------

func TestEmailVerifySendTexts(t *testing.T) {
	captchaRow := rowRule{match: "FROM captcha_codes WHERE token = ?",
		row: store.Row{"id": int64(1), "code": "AB34", "used": int64(0), "expires_at": "2099-01-01 00:00:00"}}
	// 验证码错
	db := &fakeAccounts{me: testUserRow()}
	w := callJSON(t, newAccountRouter(db, &fakeMailer{ok: true}), "email_verify_send",
		`{"token":"t","captcha_code":"ZZZZ","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"图形验证码错误或已过期","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	// 邮箱格式
	db2 := &fakeAccounts{me: testUserRow(), rows: []rowRule{captchaRow}}
	w = callJSON(t, newAccountRouter(db2, &fakeMailer{ok: true}), "email_verify_send",
		`{"token":"t","captcha_token":"tk","captcha_code":"AB34","email":"bad"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"邮箱格式不正确","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	// 已被其他账号使用
	db3 := &fakeAccounts{me: testUserRow(), rows: []rowRule{captchaRow,
		{match: "FROM users WHERE email = ? AND id <> ?", row: store.Row{"id": int64(8)}}}}
	w = callJSON(t, newAccountRouter(db3, &fakeMailer{ok: true}), "email_verify_send",
		`{"token":"t","captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"该邮箱已被其他账号使用","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	// IP 限流
	db4 := &fakeAccounts{me: testUserRow(), rows: []rowRule{captchaRow},
		vals: []valRule{{match: "WHERE ip = ?", val: int64(20), ok: true}}}
	w = callJSON(t, newAccountRouter(db4, &fakeMailer{ok: true}), "email_verify_send",
		`{"token":"t","captcha_token":"tk","captcha_code":"AB34","email":"x@y.com"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"操作过于频繁, 请稍后再试","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
}

func TestEmailVerifySendSuccessGolden(t *testing.T) {
	db := &fakeAccounts{me: testUserRow(), rows: []rowRule{{match: "FROM captcha_codes WHERE token = ?",
		row: store.Row{"id": int64(1), "code": "AB34", "used": int64(0), "expires_at": "2099-01-01 00:00:00"}}}}
	w := callJSON(t, newAccountRouter(db, &fakeMailer{ok: true}), "email_verify_send",
		`{"token":"t","captcha_token":"tk","captcha_code":"AB34","email":"new@b.com"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	want := `{"code":0,"msg":"ok","data":{"ok":true,"email":"new@b.com","expires_in":300}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("got=%s\nwant=%s", got, want)
	}
	if len(db.execs("INSERT INTO email_codes")) != 1 {
		t.Fatal("必须写 email_codes")
	}
}

func TestEmailVerifyTexts(t *testing.T) {
	me := testUserRow()
	me["id"] = int64(7)
	// 邮箱格式
	w := callJSON(t, newAccountRouter(&fakeAccounts{me: me}, &fakeMailer{}), "email_verify",
		`{"token":"t","email":"bad","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"邮箱格式不正确","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	// 验证码为空（必须排在格式校验之后）
	w = callJSON(t, newAccountRouter(&fakeAccounts{me: me}, &fakeMailer{}), "email_verify",
		`{"token":"t","email":"a@b.co","code":""}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"请输入验证码","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	// 没有可用验证码
	w = callJSON(t, newAccountRouter(&fakeAccounts{me: me}, &fakeMailer{}), "email_verify",
		`{"token":"t","email":"a@b.co","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"验证码错误或已过期","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	// 已被其他账号使用
	db := &fakeAccounts{me: me, rows: []rowRule{
		{match: "purpose = 'verify'", row: store.Row{"id": int64(3), "code": "123456", "tries": int64(0)}},
		{match: "FROM users WHERE email = ? AND id <> ?", row: store.Row{"id": int64(9)}},
	}}
	w = callJSON(t, newAccountRouter(db, &fakeMailer{}), "email_verify",
		`{"token":"t","email":"a@b.co","code":"123456"}`)
	if got, want := w.Body.String(), `{"code":1,"msg":"该邮箱已被其他账号使用","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
}

func TestEmailVerifySuccessGolden(t *testing.T) {
	me := testUserRow()
	updated := testUserRow()
	updated["email"] = "new@b.com"
	db := &fakeAccounts{me: me, rows: []rowRule{
		{match: "purpose = 'verify'", row: store.Row{"id": int64(3), "code": "123456", "tries": int64(0)}},
		{match: "SELECT * FROM users WHERE id = ?", row: updated},
	}}
	w := callJSON(t, newAccountRouter(db, &fakeMailer{}), "email_verify",
		`{"token":"t","email":"new@b.com","code":"123456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	want := `{"code":0,"msg":"ok","data":{"user":{` +
		`"id":7,"username":"alice","nickname":"爱丽丝","email":"new@b.com","email_verified":1,` +
		`"avatar":"https:\/\/q1.qlogo.cn\/g?b=qq&nk=12345&s=640","bio":"hi","role":"user",` +
		`"is_active":1,"created_at":"2026-01-02 03:04:05","tags":["vip","老用户"]}}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("got=%s\nwant=%s", got, want)
	}
	if len(db.execs("UPDATE users SET email = ?, email_verified = 1")) != 1 {
		t.Fatal("必须更新邮箱与 email_verified")
	}
	if len(db.execs("INSERT INTO notifications")) != 1 {
		t.Fatal("必须推邮箱验证成功通知")
	}
}

// ---------- user_profile ----------

func TestUserProfileFieldOrderForGuest(t *testing.T) {
	u := store.Row{
		"id": int64(42), "username": "bob", "nickname": "", "avatar": "https://a.cn/x.png",
		"bio": "bio", "role": "user", "created_at": "2026-03-04 05:06:07", "tags": "a,b",
	}
	db := &fakeAccounts{me: nil, rows: []rowRule{{match: "SELECT id, username, nickname, avatar", row: u}},
		vals: []valRule{{match: "FROM social_messages WHERE user_id = ?", val: int64(5), ok: true}}}
	w := callAction(newAccountRouter(db, &fakeMailer{}), "GET", "/api.php?action=user_profile&user_id=42", "", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("游客也应能看主页, status=%d body=%s", w.Code, w.Body.String())
	}
	want := `{"code":0,"msg":"ok","data":{` +
		`"id":42,"username":"bob","nickname":"bob","avatar":"https:\/\/a.cn\/x.png","bio":"bio",` +
		`"role":"user","created_at":"2026-03-04 05:06:07","message_count":5,"same_groups":0,` +
		`"is_me":0,"can_chat":0,"conv_id":0,"tags":["a","b"],"group_id":0,"muted":0,"mute_left":"",` +
		`"mute_reason":"","global_muted":0,"global_mute_left":"","global_mute_reason":"",` +
		`"is_admin_me":0,"can_mute":0}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("字段序/文案不一致:\n got=%s\nwant=%s", got, want)
	}
}

func TestUserProfileValidation(t *testing.T) {
	w := callAction(newAccountRouter(&fakeAccounts{}, &fakeMailer{}), "GET", "/api.php?action=user_profile&user_id=0", "", "", nil)
	if got, want := w.Body.String(), `{"code":1,"msg":"参数错误","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
	w = callAction(newAccountRouter(&fakeAccounts{}, &fakeMailer{}), "GET", "/api.php?action=user_profile&user_id=9", "", "", nil)
	if got, want := w.Body.String(), `{"code":1,"msg":"用户不存在","data":null}`; got != want {
		t.Fatalf("got=%s", got)
	}
}

func TestUserProfileSelfAndMuteTexts(t *testing.T) {
	u := store.Row{"id": int64(7), "username": "alice", "nickname": "爱丽丝", "avatar": "", "bio": "",
		"role": "user", "created_at": "2026-01-02 03:04:05", "tags": ""}
	db := &fakeAccounts{
		me: testUserRow(),
		rows: []rowRule{{match: "SELECT id, username, nickname, avatar", row: u},
			{match: "FROM social_user_mutes", row: store.Row{"id": int64(1), "group_id": int64(3), "until_at": "", "reason": "刷屏"}}},
		vals: []valRule{{match: "FROM social_messages WHERE user_id = ?", val: int64(1), ok: true}},
	}
	w := callAction(newAccountRouter(db, &fakeMailer{}), "GET", "/api.php?action=user_profile&user_id=7&token=t", "", "", nil)
	body := w.Body.String()
	for _, frag := range []string{`"is_me":1`, `"can_chat":0`, `"muted":1`, `"mute_left":"永久"`, `"mute_reason":"刷屏"`} {
		if !strings.Contains(body, frag) {
			t.Fatalf("缺 %s: %s", frag, body)
		}
	}
}

// ---------- logout ----------

func TestLogoutUsesParamTokenOnly(t *testing.T) {
	// 只带 Bearer 头：PHP 不认，所以什么都不删。
	db := &fakeAccounts{}
	w := callAction(newAccountRouter(db, &fakeMailer{}), "POST", "/api.php?action=logout", "", "",
		map[string]string{"Authorization": "Bearer abcdef"})
	if w.Code != http.StatusOK || w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(db.execs("DELETE FROM sessions")) != 0 {
		t.Fatal("PHP 的 logout 只看 param('token')，Bearer 头不该触发删除")
	}

	// 带 token 参数：删 sessions 并清 users.token
	db2 := &fakeAccounts{}
	w = callJSON(t, newAccountRouter(db2, &fakeMailer{}), "logout", `{"token":"abcdef"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("body=%s", w.Body.String())
	}
	del := db2.execs("DELETE FROM sessions WHERE token = ?")
	upd := db2.execs("UPDATE users SET token = NULL WHERE token = ?")
	if len(del) != 1 || len(upd) != 1 {
		t.Fatalf("del=%d upd=%d", len(del), len(upd))
	}
	if del[0].Args[0] != "abcdef" || upd[0].Args[0] != "abcdef" {
		t.Fatalf("参数不对: %#v %#v", del[0].Args, upd[0].Args)
	}
}

// ---------- 工具与辅助 ----------

func TestUserTagsArrMatchesPHP(t *testing.T) {
	cases := []struct {
		raw  any
		want []string
	}{
		{nil, []string{}},
		{"", []string{}},
		{"  ", []string{}},
		{"a, b ,c", []string{"a", "b", "c"}},
		{"a,a,b", []string{"a", "b"}},
		{"a,b,c,d,e,f", []string{"a", "b", "c", "d", "e"}},
		{strings.Repeat("字", 11) + ",ok", []string{"ok"}},
	}
	for _, c := range cases {
		got := userTagsArr(store.Row{"tags": c.raw})
		if len(got) != len(c.want) {
			t.Fatalf("tags=%v 期望 %v", got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("tags=%v 期望 %v", got, c.want)
			}
		}
	}
}

func TestMuteLeftTextMatchesPHP(t *testing.T) {
	now := nowUnix()
	cases := []struct {
		until string
		want  string
	}{
		{"", "永久"},
		{"2000-01-01 00:00:00", ""},
	}
	for _, c := range cases {
		if got := muteLeftText(store.Row{"until_at": c.until}); got != c.want {
			t.Fatalf("until=%q got=%q want=%q", c.until, got, c.want)
		}
	}
	if got := muteLeftText(nil); got != "" {
		t.Fatalf("nil mute 应返回空串, 实际 %q", got)
	}
	// 未来的时间：文案必须是「剩余 N 天/小时/分钟」三种之一，且首字是「剩」。
	rows := map[string]string{}
	rows["2099-01-01 00:00:00"] = "剩余"
	for until := range rows {
		if got := muteLeftText(store.Row{"until_at": until}); !strings.HasPrefix(got, "剩余 ") {
			t.Fatalf("got=%q", got)
		}
	}
	_ = now
}

func TestQQAvatarFromEmail(t *testing.T) {
	cases := map[string]string{
		"12345@qq.com":         "https://q1.qlogo.cn/g?b=qq&nk=12345&s=640",
		"123456789@vip.qq.com": "https://q1.qlogo.cn/g?b=qq&nk=123456789&s=640",
		"1234@qq.com":          "",
		"abc@qq.com":           "",
		"12345@example.com":    "",
		"12345@QQ.COM":         "https://q1.qlogo.cn/g?b=qq&nk=12345&s=640",
	}
	for in, want := range cases {
		if got := qqAvatarFromEmail(in); got != want {
			t.Errorf("qqAvatarFromEmail(%q)=%q want=%q", in, got, want)
		}
	}
}

func TestClientIPOrderMatchesPHP(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "10.0.0.9:5555"
	if got := clientIP(req); got != "10.0.0.9" {
		t.Fatalf("无 XFF 应用 REMOTE_ADDR 去端口, 实际 %q", got)
	}
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(req); got != "1.2.3.4, 5.6.7.8" {
		t.Fatalf("XFF 必须整串（不切第一个）, 实际 %q", got)
	}
}

// 数据库出错时：500 + 文案里不带 Go 文件名/行号（与其它原生 action 的约定一致）。
func TestAccountDBErrorIs500WithoutPathLeak(t *testing.T) {
	db := &fakeAccounts{}
	db.meErr = context.DeadlineExceeded
	w := callAction(newAccountRouter(db, &fakeMailer{}), "GET", "/api.php?action=user_me&token=t", "", "", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":500`) {
		t.Fatalf("body=%s", body)
	}
	if strings.Contains(body, ".go") || strings.Contains(body, "@ ") {
		t.Fatalf("文案里泄露了 Go 路径: %s", body)
	}
}

// phpjsonTimeAgo 生成「N 秒之前的 MySQL DATETIME 字符串」，供限流用例构造数据。
// 与生产一致：按 Asia/Shanghai 解释（config.LocalZone）。
func phpjsonTimeAgo(sec int) string {
	return time.Now().In(config.LocalZone()).Add(-time.Duration(sec) * time.Second).Format("2006-01-02 15:04:05")
}
