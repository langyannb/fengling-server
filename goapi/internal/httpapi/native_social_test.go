package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 3 群聊 action 的单测：全部走脚本化假数据访问层，不连真实 MySQL/Redis。
//
// 覆盖重点（派单书要求）：
//   - social_send 的校验顺序（顺序错了用户看到的第一条错误就变了）与逐字文案；
//   - at 解析（含「尾随逗号在读取侧会变成 @所有人」这条 PHP 怪癖，不许修好）；
//   - 禁言组合分支（全员禁言 / 个人禁言 / 剩永久/天/小时/分钟）；
//   - social_read 的未读归零与 GREATEST 不回退；
//   - social_recall 的时间窗（5 分钟）与权限（本人 / 管理员 / 他人）；
//   - social_group_join/leave 的成员计数与系统消息；
//   - 通知三兄弟（@所有人 / @某人 / 普通合并 / 免打扰跳过）与 notify_merge 的合并语义；
//   - 每个 action 的 JSON 字段序与逐字文案 golden。

// ---------- 假数据访问层 ----------

// socialRule 是按 SQL 子串匹配的多行规则（QueryRow 取第一行）。
type socialRule struct {
	match string
	rows  []store.Row
	err   error
}

type socialFakeDB struct {
	me     store.Row
	meErr  error
	rules  []socialRule
	vals   []valRule
	rowFn  func(q string, args []any) (store.Row, error)
	allFn  func(q string, args []any) ([]store.Row, error)
	valFn  func(q string, args []any) (any, bool, error)
	execFn func(q string, args []any) (store.ExecResult, error)

	insertID     int64
	rowsAffected int64
	zeroRows     bool // 置 true 时 Exec 返回 RowsAffected=0（INSERT IGNORE 被忽略 / DELETE 未命中）
	execErr      error

	calls []fakeCall
}

func (f *socialFakeDB) CurrentUserRow(_ context.Context, token string) (store.Row, error) {
	f.calls = append(f.calls, fakeCall{Method: "CurrentUserRow", Args: []any{token}})
	if f.meErr != nil {
		return nil, f.meErr
	}
	if token == "" {
		return nil, nil
	}
	return f.me, nil
}

func (f *socialFakeDB) QueryRow(_ context.Context, q string, args ...any) (store.Row, error) {
	f.calls = append(f.calls, fakeCall{Method: "QueryRow", Query: q, Args: args})
	if f.rowFn != nil {
		return f.rowFn(q, args)
	}
	for _, r := range f.rules {
		if strings.Contains(q, r.match) {
			if r.err != nil {
				return nil, r.err
			}
			if len(r.rows) == 0 {
				return nil, nil
			}
			return r.rows[0], nil
		}
	}
	return nil, nil
}

func (f *socialFakeDB) QueryAll(_ context.Context, q string, args ...any) ([]store.Row, error) {
	f.calls = append(f.calls, fakeCall{Method: "QueryAll", Query: q, Args: args})
	if f.allFn != nil {
		return f.allFn(q, args)
	}
	for _, r := range f.rules {
		if strings.Contains(q, r.match) {
			return r.rows, r.err
		}
	}
	return nil, nil
}

func (f *socialFakeDB) QueryValue(_ context.Context, q string, args ...any) (any, bool, error) {
	f.calls = append(f.calls, fakeCall{Method: "QueryValue", Query: q, Args: args})
	if f.valFn != nil {
		return f.valFn(q, args)
	}
	for _, r := range f.vals {
		if strings.Contains(q, r.match) {
			return r.val, r.ok, r.err
		}
	}
	return nil, false, nil
}

func (f *socialFakeDB) Exec(_ context.Context, q string, args ...any) (store.ExecResult, error) {
	f.calls = append(f.calls, fakeCall{Method: "Exec", Query: q, Args: args})
	if f.execFn != nil {
		return f.execFn(q, args)
	}
	if f.execErr != nil {
		return store.ExecResult{}, f.execErr
	}
	n := f.rowsAffected
	if f.zeroRows {
		n = 0
	} else if n == 0 {
		n = 1
	}
	return store.ExecResult{LastInsertID: f.insertID, RowsAffected: n}, nil
}

func (f *socialFakeDB) execs(match string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == "Exec" && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

func (f *socialFakeDB) queries(match string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method != "Exec" && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

// fakeCols 是列探测的假实现（key = "表.列"）。
type fakeCols map[string]bool

func (f fakeCols) HasColumn(_ context.Context, table, column string) bool {
	return f[table+"."+column]
}

// migratedCols 模拟「视频迁移已经跑过」的库（video / msg_type 列都在）。
func migratedCols() fakeCols {
	return fakeCols{"social_messages.video": true, "social_messages.msg_type": true}
}

// legacyCols 模拟「视频迁移还没跑」的库。
func legacyCols() fakeCols { return fakeCols{} }

type fixedVideoCfg struct{ cfg store.VideoConfig }

func (f fixedVideoCfg) VideoConfig(context.Context) store.VideoConfig { return f.cfg }

const testS3Public = "https://fenglin.cn-nb1.rains3.com"

func newSocialRouter(db accountsDB, cols columnProbe, video *store.VideoConfig) *Router {
	cfg := config.Load()
	cfg.S3PublicURL = testS3Public
	env := Env{Cfg: cfg, Log: testLogger(), Accounts: db}
	if cols != nil {
		env.Cols = cols
	}
	if video != nil {
		env.VideoCfg = fixedVideoCfg{cfg: *video}
	}
	return New(env)
}

// srow 是构造 store.Row 的简写（值用 int / string / nil，与驱动取出的形态一致）。
func srow(kv ...any) store.Row {
	r := store.Row{}
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i].(string)] = kv[i+1]
	}
	return r
}

func socialMe() store.Row {
	return srow("id", 7, "username", "alice", "nickname", "爱丽丝", "role", "user", "is_active", 1)
}

func socialAdmin() store.Row {
	return srow("id", 1, "username", "root", "nickname", "管理员", "role", "admin", "is_active", 1)
}

func socialGroupRow() store.Row {
	return srow("id", 5, "name", "测试群", "icon", "", "description", "", "notice", "",
		"sort_order", 0, "is_active", 1, "all_muted", 0, "created_at", "2026-01-01 10:00:00")
}

func callSocial(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json", nil)
}

// dataOf 解出响应里的 data 字段（只用于断言字段值；字段顺序用整串 golden 断言）。
func dataOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var shell struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &shell); err != nil {
		t.Fatalf("响应不是合法 JSON: %s", w.Body.String())
	}
	return shell.Data
}

func assertError(t *testing.T, w *httptest.ResponseRecorder, httpStatus, code int, msg string) {
	t.Helper()
	if w.Code != httpStatus {
		t.Fatalf("HTTP 状态码=%d, 期望 %d, body=%s", w.Code, httpStatus, w.Body.String())
	}
	want := `{"code":` + phpNum(int64(code)) + `,"msg":` + mustJSONString(msg) + `,"data":null}`
	if got := w.Body.String(); got != want {
		t.Fatalf("错误响应逐字不一致\n got=%s\nwant=%s", got, want)
	}
}

// mustJSONString 用生产同一条 phpjson 编码路径转义（这样 golden 断言才真的等价于 PHP）。
func mustJSONString(s string) string {
	return string(phpjson.AppendString(nil, s))
}

// ---------- 纯函数：PHP 语义复刻 ----------

func TestPhpSplitAtMatchesPregSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{""}},
		{"1,2", []string{"1", "2"}},
		{"1, 2", []string{"1", "2"}},
		{"1,2,", []string{"1", "2", ""}},
		{",,1", []string{"", "1"}}, // [,\s]+ 把连续分隔符当一个
		{"  ", []string{"", ""}},
		{"1\t2\n3\r\n4", []string{"1", "2", "3", "4"}},
		{"7", []string{"7"}},
	}
	for _, tc := range cases {
		got := phpSplitAt(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("phpSplitAt(%q)=%v, 期望 %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("phpSplitAt(%q)=%v, 期望 %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestParseAtParamMatchesPHP(t *testing.T) {
	cases := []struct {
		body string
		want []int64
	}{
		{`{"at":"8,9,"}`, []int64{8, 9}},      // 尾随空段 intval=0 被 >0 过滤
		{`{"at":"8,9, "}`, []int64{8, 9}},     // 同上（空格分隔）
		{`{"at":"0,7,8"}`, []int64{8}},        // 0 与自己都过滤
		{`{"at":"8,8,9"}`, []int64{8, 9}},     // array_unique
		{`{"at":[8,"9",0,7]}`, []int64{8, 9}}, // JSON 数组
		{`{"at":5}`, []int64{}},               // 标量在 PHP 里被整个丢弃
		{`{"at":"abc"}`, []int64{}},           // intval("abc")=0
		{`{"at":"-3,8"}`, []int64{8}},         // 负数过滤
		{`{"at":"8.9"}`, []int64{8}},          // intval("8.9")=8
		{`{"at":""}`, []int64{}},              // 空串 -> 一个空段 -> 0
		{`{}`, []int64{}},                     // 没传
		{`{"at":[[9]]}`, []int64{1}},          // PHP 的 intval(非空数组) = 1（照抄这条怪癖）
	}
	for _, tc := range cases {
		got := parseAtParam(atParamCtx(t, tc.body), 7)
		if len(got) != len(tc.want) {
			t.Fatalf("parseAtParam(%s)=%v, 期望 %v", tc.body, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseAtParam(%s)=%v, 期望 %v", tc.body, got, tc.want)
			}
		}
	}
}

func atParamCtx(t *testing.T, body string) *Ctx {
	t.Helper()
	rt := newSocialRouter(&socialFakeDB{}, nil, nil)
	req := httptest.NewRequest("POST", "/api.php?action=social_send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return rt.env.newCtx(httptest.NewRecorder(), req, "social_send")
}

func TestSplitIntsKeepsTrailingZero(t *testing.T) {
	got := splitInts("7,")
	if len(got) != 2 || got[0] != 7 || got[1] != 0 {
		t.Fatalf("splitInts(\"7,\")=%v, 期望 [7 0]（PHP 的 @所有人 标记位）", got)
	}
	if len(splitInts("")) != 0 {
		t.Fatal("空串必须返回空数组")
	}
}

func TestMbSubstrIsCharacterBased(t *testing.T) {
	s := strings.Repeat("风", 100)
	if got := mbSubstr(s, 0, 60); len([]rune(got)) != 60 {
		t.Fatalf("mbSubstr 长度=%d", len([]rune(got)))
	}
	if got := mbSubstr("abc", 5, 3); got != "" {
		t.Fatalf("越界 start 必须返回空串, got=%q", got)
	}
}

// 读取侧的 PHP 怪癖：at_users 尾随逗号会产生 0，而 0 = @所有人。
func TestMentionMapTrailingCommaMeansAtAll(t *testing.T) {
	db := &socialFakeDB{
		me: socialMe(),
		rules: []socialRule{{match: "FROM social_messages m", rows: []store.Row{
			srow("id", 101, "group_id", 5, "at_users", "8"),
			srow("id", 102, "group_id", 5, "at_users", "7,"),
			srow("id", 103, "group_id", 6, "at_users", "0,7"),
			srow("id", 104, "group_id", 6, "at_users", "9"),
		}}},
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	s := rt.newSocialScope(atParamCtx(t, `{}`))
	got := s.myMentionMap(7)
	if got[5].atAll != 1 || got[5].atAllFirst != 102 {
		t.Fatalf("at_users=\"7,\" 必须被当成 @所有人（PHP 怪癖不许修）: %+v", got[5])
	}
	// 注意：空段变成 0（=> @所有人），但 7 本身也在，所以 at_me 同样 +1 —— PHP 就是这个结果。
	if got[5].atMe != 1 || got[5].atMeFirst != 102 {
		t.Fatalf("at_users=\"7,\" 必须同时算 @我: %+v", got[5])
	}
	if got[6].atAll != 1 || got[6].atMe != 1 || got[6].atAllFirst != 103 || got[6].atMeFirst != 103 {
		t.Fatalf("at_users=\"0,7\" 必须同时算 @所有人 + @我: %+v", got[6])
	}
	if got[6].atMe != 1 {
		t.Fatalf("at_me 计数错误: %+v", got[6])
	}
}

// ---------- social_send ----------

// sendDB 造一个「happy path」的发送环境：群、成员、无历史消息、三个其他有效用户（10 号开了免打扰）。
func sendDB(me store.Row, group store.Row) *socialFakeDB {
	return &socialFakeDB{
		me:       me,
		insertID: 123,
		rules: []socialRule{
			{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{group}},
			{match: "SELECT 1 FROM social_group_members", rows: []store.Row{srow("1", 1)}},
			{match: "SELECT created_at FROM social_messages WHERE user_id", rows: nil},
			{match: "SELECT id FROM social_messages WHERE id = ?", rows: nil},
			{match: "SELECT id FROM users WHERE is_active = 1 AND id <> ?", rows: []store.Row{
				srow("id", 8), srow("id", 9), srow("id", 10),
			}},
			{match: "SELECT id FROM users WHERE id IN", rows: []store.Row{srow("id", 8), srow("id", 9)}},
			{match: "SELECT user_id FROM social_mutes WHERE group_id", rows: []store.Row{srow("user_id", 10)}},
			{match: "SELECT name FROM social_groups WHERE id = ?", rows: []store.Row{srow("name", "测试群")}},
		},
	}
}

// assertSendResponse 校验 social_send 的响应壳与字段顺序（created_at 是动态值，单独抠出来）。
func assertSendResponse(t *testing.T, w *httptest.ResponseRecorder, id, atAll, quoteID int64) string {
	t.Helper()
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP=%d body=%s", w.Code, body)
	}
	const pre = `{"code":0,"msg":"ok","data":{"id":`
	if !strings.HasPrefix(body, pre) {
		t.Fatalf("响应前缀不符: %s", body)
	}
	rest := body[len(pre):]
	parts := strings.SplitN(rest, `,`, 3)
	if len(parts) != 3 {
		t.Fatalf("响应结构不符: %s", body)
	}
	if parts[0] != phpNum(id) {
		t.Fatalf("id=%s 期望 %d", parts[0], id)
	}
	if parts[1] != `"at_all":`+phpNum(atAll) {
		t.Fatalf("at_all 字段不符: %s", parts[1])
	}
	// 剩下是 "quote_id":N,"created_at":"..."}}
	wantQuote := `"quote_id":` + phpNum(quoteID) + `,"created_at":"`
	if !strings.HasPrefix(parts[2], wantQuote) {
		t.Fatalf("quote_id/created_at 位置不符: %s", parts[2])
	}
	rest2 := strings.TrimSuffix(parts[2][len(wantQuote):], `"}}`)
	if _, err := time.ParseInLocation("2006-01-02 15:04:05", rest2, config.LocalZone()); err != nil {
		t.Fatalf("created_at 不是 MySQL DATETIME 字符串: %q", rest2)
	}
	return rest2
}

// 普通消息：给除我外的有效用户合并推「「X」新消息」，跳过开了免打扰的 10 号。
func TestSocialSendPlainMessageMergesAndSkipsMuted(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"你好"}`)
	assertSendResponse(t, w, 123, 0, 0)

	ins := db.execs("INSERT INTO social_messages")
	if len(ins) != 1 {
		t.Fatalf("INSERT social_messages 次数=%d", len(ins))
	}
	q := ins[0].Query
	if !strings.Contains(q, "msg_type") || !strings.Contains(q, "video_duration") {
		t.Fatalf("迁移后的库必须带上 video*/msg_type 列: %s", q)
	}
	args := ins[0].Args
	if len(args) != 15 {
		t.Fatalf("INSERT 参数个数=%d, 期望 15: %v", len(args), args)
	}
	if args[0] != int64(5) || args[1] != int64(7) || args[2] != "你好" {
		t.Fatalf("INSERT 前三个参数=%v", args[:3])
	}
	if args[6] != "" { // at_users
		t.Fatalf("没 @ 任何人时 at_users 必须是空串, got=%v", args[6])
	}
	if phpInt(args[7]) != 0 {
		t.Fatalf("quote_id 必须为 0, got=%v", args[7])
	}
	if args[13] != "" {
		t.Fatalf("msg_type 必须是空串, got=%v", args[13])
	}
	if phpInt(args[14]) != 0 {
		t.Fatalf("is_recalled 必须为 0, got=%v", args[14])
	}

	merges := db.execs("INSERT INTO notifications")
	if len(merges) != 2 {
		t.Fatalf("合并通知次数=%d, 期望 2（10 号开了免打扰必须跳过）: %v", len(merges), merges)
	}
	for i, want := range []int64{8, 9} {
		a := merges[i].Args
		if a[0] != want || a[1] != "「测试群」新消息" || a[2] != "爱丽丝: 你好" || a[3] != "social" || a[4] != "msg:5:123" {
			t.Fatalf("第 %d 条合并通知参数=%v", i, a)
		}
	}
}

// @某人：单独推「有人在「X」@了你」（带 link），并且不再走合并通知。
func TestSocialSendAtUsersPushesWithLink(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"在吗","at":"8,9"}`)
	assertSendResponse(t, w, 123, 0, 0)

	ins := db.execs("INSERT INTO social_messages")
	if ins[0].Args[6] != "8,9" {
		t.Fatalf("at_users=%v, 期望 \"8,9\"", ins[0].Args[6])
	}
	pushes := db.execs("INSERT INTO notifications")
	if len(pushes) != 2 {
		t.Fatalf("@ 通知次数=%d, 期望 2（被 @ 的人不再收到合并通知）", len(pushes))
	}
	for i, want := range []int64{8, 9} {
		a := pushes[i].Args
		if a[0] != want || a[1] != "有人在「测试群」@了你" || a[2] != "在吗" || a[3] != "social" || a[4] != "msg:5:123" {
			t.Fatalf("第 %d 条 @ 通知参数=%v", i, a)
		}
	}
}

// @所有人（仅管理员）：link 为空，且不再走合并通知。
func TestSocialSendAtAllAdminPushesWithoutLink(t *testing.T) {
	db := sendDB(socialAdmin(), socialGroupRow())
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"通知","at_all":1}`)
	assertSendResponse(t, w, 123, 1, 0)

	ins := db.execs("INSERT INTO social_messages")
	if ins[0].Args[6] != "0" {
		t.Fatalf("at_users 必须是 \"0\"（0 = 所有人）, got=%v", ins[0].Args[6])
	}
	pushes := db.execs("INSERT INTO notifications")
	if len(pushes) != 3 {
		t.Fatalf("@所有人 通知次数=%d, 期望 3", len(pushes))
	}
	for i, want := range []int64{8, 9, 10} {
		a := pushes[i].Args
		if a[0] != want || a[1] != "「测试群」有人 @了所有人" || a[2] != "通知" || a[3] != "social" || a[4] != "" {
			t.Fatalf("第 %d 条 @所有人 通知参数=%v", i, a)
		}
	}
}

// 内容里写 @所有人：只有管理员才算数。
func TestSocialSendAtAllInContentRequiresAdmin(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"@所有人 集合"}`)
	assertSendResponse(t, w, 123, 0, 0)
	for _, e := range db.execs("INSERT INTO notifications") {
		if strings.Contains(phpStr(a0(e.Args)), "有人 @了所有人") {
			t.Fatalf("普通用户写 @所有人 不该触发全量通知: %v", e.Args)
		}
	}

	db2 := sendDB(socialAdmin(), socialGroupRow())
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_send", `{"token":"t","group_id":5,"content":"@所有人 集合"}`)
	assertSendResponse(t, w2, 123, 1, 0)
	if db2.execs("INSERT INTO social_messages")[0].Args[6] != "0" {
		t.Fatal("管理员的 at_users 必须含 0")
	}
}

func a0(args []any) any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

// 尾随逗号：at 解析时 0/空段会被过滤，所以存进库里的 at_users 不带尾随逗号。
func TestSocialSendTrailingCommaInAtIsFilteredWhenStoring(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, migratedCols(), nil)
	callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"hi","at":"8,9,"}`)
	if got := db.execs("INSERT INTO social_messages")[0].Args[6]; got != "8,9" {
		t.Fatalf("at_users=%v, 期望 \"8,9\"", got)
	}
}

// 视频消息：msg_type=video、video* 列都写进去、通知文案是 [视频]。
func TestSocialSendVideoMessageGolden(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, migratedCols(), nil)
	vurl := testS3Public + "/chat/abc.mp4"
	w := callSocial(t, rt, "social_send",
		`{"token":"t","group_id":5,"content":"","video":"`+vurl+`","video_w":1080,"video_h":1920,"video_duration":12,"video_size":3456}`)
	assertSendResponse(t, w, 123, 0, 0)
	ins := db.execs("INSERT INTO social_messages")[0]
	a := ins.Args
	if a[8] != vurl || a[9] != int64(1080) || a[10] != int64(1920) || a[11] != int64(12) || a[12] != int64(3456) {
		t.Fatalf("video 列参数=%v", a[8:13])
	}
	if a[13] != "video" {
		t.Fatalf("msg_type=%v, 期望 video", a[13])
	}
	notif := db.execs("INSERT INTO notifications")
	if len(notif) != 2 {
		t.Fatalf("视频消息的通知条数=%d, 期望 2（10 号免打扰跳过）", len(notif))
	}
	for _, e := range notif {
		body := phpStr(e.Args[2])
		if body != "[视频]" && body != "爱丽丝: [视频]" {
			t.Fatalf("视频消息的通知正文必须是 [视频]，got=%v", e.Args[2])
		}
	}
}

// 未迁移的库（没有 video 列）：带 video 的消息必须报同一句中文。
func TestSocialSendVideoWithoutColumn(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, legacyCols(), nil)
	w := callSocial(t, rt, "social_send",
		`{"token":"t","group_id":5,"video":"`+testS3Public+`/chat/abc.mp4"}`)
	assertError(t, w, http.StatusBadRequest, 1, "服务端未完成视频迁移, 请联系管理员")
}

// 未迁移的库：纯文字消息的 INSERT 自动退回旧列组合（不带 video*/msg_type）。
func TestSocialSendLegacyColumnsFallback(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	rt := newSocialRouter(db, legacyCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"旧库"}`)
	assertSendResponse(t, w, 123, 0, 0)
	ins := db.execs("INSERT INTO social_messages")[0]
	if strings.Contains(ins.Query, "msg_type") || strings.Contains(ins.Query, "video") {
		t.Fatalf("旧库不该写新列: %s", ins.Query)
	}
	if len(ins.Args) != 9 {
		t.Fatalf("旧库 INSERT 参数个数=%d, 期望 9: %v", len(ins.Args), ins.Args)
	}
}

// 视频功能关闭时不能再发视频消息。
func TestSocialSendVideoDisabled(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	cfg := store.VideoConfigDefault()
	cfg.Enabled = 0
	rt := newSocialRouter(db, migratedCols(), &cfg)
	w := callSocial(t, rt, "social_send",
		`{"token":"t","group_id":5,"video":"`+testS3Public+`/chat/abc.mp4"}`)
	assertError(t, w, http.StatusBadRequest, 1, "视频消息功能未开启")
}

// 引用回复：同群才认，否则静默置 0。
func TestSocialSendQuoteMustBeSameGroup(t *testing.T) {
	group := socialGroupRow()
	db := sendDB(socialMe(), group)
	db.rules = append([]socialRule{{match: "SELECT id FROM social_messages WHERE id = ? AND group_id = ?", rows: []store.Row{srow("id", 99)}}}, db.rules...)
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"引用","quote_id":99}`)
	assertSendResponse(t, w, 123, 0, 99)
	if db.execs("INSERT INTO social_messages")[0].Args[7] != int64(99) {
		t.Fatal("同群引用必须写进 quote_id")
	}

	db2 := sendDB(socialMe(), group)
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_send", `{"token":"t","group_id":5,"content":"引用","quote_id":99}`)
	assertSendResponse(t, w2, 123, 0, 0)
	if db2.execs("INSERT INTO social_messages")[0].Args[7] != int64(0) {
		t.Fatal("不是同群的消息必须静默置 0")
	}
}

// 限流：2 秒/群，用「我自己在该群最后一条消息的 created_at」判定。
func TestSocialSendRateLimitWindow(t *testing.T) {
	db := sendDB(socialMe(), socialGroupRow())
	db.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "SELECT created_at FROM social_messages WHERE user_id") {
			return phpjsonTimeAgo(1), true, nil
		}
		return nil, false, nil
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_send", `{"token":"t","group_id":5,"content":"快的"}`)
	assertError(t, w, http.StatusBadRequest, 1, "发送太快了, 请稍后再试")

	db2 := sendDB(socialMe(), socialGroupRow())
	db2.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "SELECT created_at FROM social_messages WHERE user_id") {
			return phpjsonTimeAgo(3), true, nil
		}
		return nil, false, nil
	}
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_send", `{"token":"t","group_id":5,"content":"慢慢来"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("3 秒前发过必须放行: %s", w2.Body.String())
	}
}

// 校验顺序：每一步失败时用户看到的第一条错误必须与 PHP 一致。
func TestSocialSendValidationOrderAndTexts(t *testing.T) {
	longContent := strings.Repeat("字", 501)

	noLogin := &socialFakeDB{}
	wNoLogin := callSocial(t, newSocialRouter(noLogin, migratedCols(), nil), "social_send", `{"group_id":5,"content":"x"}`)
	assertError(t, wNoLogin, http.StatusUnauthorized, 401, "登录已失效")

	banned := sendDB(srow("id", 7, "username", "alice", "nickname", "爱丽丝", "role", "user", "is_active", 0), socialGroupRow())
	wBanned := callSocial(t, newSocialRouter(banned, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wBanned, http.StatusBadRequest, 403, "你已被封禁")

	missing := &socialFakeDB{me: socialMe(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: nil}}}
	wMissing := callSocial(t, newSocialRouter(missing, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wMissing, http.StatusBadRequest, 1, "群组不存在或已停用")

	inactive := socialGroupRow()
	inactive["is_active"] = 0
	inactiveDB := &socialFakeDB{me: socialMe(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{inactive}}}}
	wInactive := callSocial(t, newSocialRouter(inactiveDB, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wInactive, http.StatusBadRequest, 1, "群组不存在或已停用")

	// 「先加入群聊」必须排在「全员禁言」之前：非成员 + all_muted=1 时报的是前者。
	allMuted := socialGroupRow()
	allMuted["all_muted"] = 1
	notMember := sendDB(socialMe(), allMuted)
	notMember.rules = []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{allMuted}},
		{match: "SELECT 1 FROM social_group_members", rows: nil},
	}
	wNotMember := callSocial(t, newSocialRouter(notMember, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wNotMember, http.StatusBadRequest, 403, "请先加入群聊再发言")

	memberAllMuted := sendDB(socialMe(), allMuted)
	wAllMuted := callSocial(t, newSocialRouter(memberAllMuted, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wAllMuted, http.StatusBadRequest, 403, "群主已开启全体禁言, 暂时不能发言")

	forever := sendDB(socialMe(), socialGroupRow())
	forever.rules = append([]socialRule{{match: "FROM social_user_mutes", rows: []store.Row{
		srow("id", 1, "group_id", 5, "until_at", nil, "reason", " spam ")}}}, forever.rules...)
	wMute := callSocial(t, newSocialRouter(forever, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wMute, http.StatusBadRequest, 403, "你已被禁言 (永久), 原因: spam")

	hour := sendDB(socialMe(), socialGroupRow())
	hour.rules = append([]socialRule{{match: "FROM social_user_mutes", rows: []store.Row{
		srow("id", 2, "group_id", 5, "until_at", phpjsonTimeAgo(-3700), "reason", "")}}}, hour.rules...)
	wHour := callSocial(t, newSocialRouter(hour, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	assertError(t, wHour, http.StatusBadRequest, 403, "你已被禁言 (剩余 1 小时)")

	// 管理员不受禁言/全员禁言限制：直接进到内容校验。
	adminSkip := sendDB(socialAdmin(), allMuted)
	wAdmin := callSocial(t, newSocialRouter(adminSkip, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"at_all":1}`)
	assertError(t, wAdmin, http.StatusBadRequest, 1, "消息内容不能为空")

	imgBad := sendDB(socialMe(), socialGroupRow())
	wImgBad := callSocial(t, newSocialRouter(imgBad, migratedCols(), nil), "social_send",
		`{"token":"t","group_id":5,"image":"https://evil.example.com/chat/x.jpg"}`)
	assertError(t, wImgBad, http.StatusBadRequest, 1, "图片地址不合法")

	vidBad := sendDB(socialMe(), socialGroupRow())
	wVidBad := callSocial(t, newSocialRouter(vidBad, migratedCols(), nil), "social_send",
		`{"token":"t","group_id":5,"video":"https://evil.example.com/chat/x.mp4"}`)
	assertError(t, wVidBad, http.StatusBadRequest, 1, "视频地址不合法")

	// 无条件调用 video_meta_params：纯文字消息带了越界参数也要报错。
	metaW := sendDB(socialMe(), socialGroupRow())
	wMetaW := callSocial(t, newSocialRouter(metaW, migratedCols(), nil), "social_send",
		`{"token":"t","group_id":5,"content":"hi","video_w":20001}`)
	assertError(t, wMetaW, http.StatusBadRequest, 1, "视频尺寸参数不合法")

	metaD := sendDB(socialMe(), socialGroupRow())
	wMetaD := callSocial(t, newSocialRouter(metaD, migratedCols(), nil), "social_send",
		`{"token":"t","group_id":5,"content":"hi","video_duration":3601}`)
	assertError(t, wMetaD, http.StatusBadRequest, 1, "视频时长参数不合法 (最长 60 分钟)")

	metaS := sendDB(socialMe(), socialGroupRow())
	wMetaS := callSocial(t, newSocialRouter(metaS, migratedCols(), nil), "social_send",
		`{"token":"t","group_id":5,"content":"hi","video_size":2147483648}`)
	assertError(t, wMetaS, http.StatusBadRequest, 1, "视频大小参数不合法")

	empty := sendDB(socialMe(), socialGroupRow())
	wEmpty := callSocial(t, newSocialRouter(empty, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"   "}`)
	assertError(t, wEmpty, http.StatusBadRequest, 1, "消息内容不能为空")

	tooLong := sendDB(socialMe(), socialGroupRow())
	wLong := callSocial(t, newSocialRouter(tooLong, migratedCols(), nil), "social_send",
		`{"token":"t","group_id":5,"content":"`+longContent+`"}`)
	assertError(t, wLong, http.StatusBadRequest, 1, "消息不能超过 500 个字")
}

// 数据库异常：500 + 不泄露 Go 路径。
func TestSocialSendDBErrorIs500WithoutPathLeak(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), meErr: context.DeadlineExceeded}
	w := callSocial(t, newSocialRouter(db, migratedCols(), nil), "social_send", `{"token":"t","group_id":5,"content":"x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"code":500`) ||
		strings.Contains(w.Body.String(), ".go") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

// ---------- social_read ----------

func TestSocialReadDefaultsToMaxIDAndReadsBack(t *testing.T) {
	db := &socialFakeDB{me: socialMe(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}},
		valFn: func(q string, _ []any) (any, bool, error) {
			switch {
			case strings.Contains(q, "SELECT COALESCE(MAX(id), 0)"):
				return int64(42), true, nil
			case strings.Contains(q, "SELECT last_read_id FROM social_reads"):
				return int64(42), true, nil
			}
			return nil, false, nil
		}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_read", `{"token":"t","group_id":5}`)
	want := `{"code":0,"msg":"ok","data":{"ok":true,"last_read_id":42}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	ins := db.execs("INSERT INTO social_reads")
	if len(ins) != 1 || !strings.Contains(ins[0].Query, "GREATEST(last_read_id, VALUES(last_read_id))") {
		t.Fatalf("必须照抄 GREATEST 语义: %v", ins)
	}
	if ins[0].Args[0] != int64(7) || ins[0].Args[1] != int64(5) || ins[0].Args[2] != int64(42) {
		t.Fatalf("INSERT social_reads 参数=%v", ins[0].Args)
	}
}

// 传更小的 last_id：库里仍是较大的值（GREATEST），响应回读真实值。
func TestSocialReadGreatestNeverGoesBackwards(t *testing.T) {
	db := &socialFakeDB{me: socialMe(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}},
		valFn: func(q string, _ []any) (any, bool, error) {
			if strings.Contains(q, "SELECT last_read_id FROM social_reads") {
				return int64(99), true, nil
			}
			return nil, false, nil
		}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_read", `{"token":"t","group_id":5,"last_id":10}`)
	if got := dataOf(t, w)["last_read_id"]; got != float64(99) {
		t.Fatalf("last_read_id=%v, 期望 99（GREATEST 不回退）", got)
	}
	if _, ok := dataOf(t, w)["ok"]; !ok {
		t.Fatal("缺少 ok 字段")
	}
}

func TestSocialReadUnreadCountZeroAfterRead(t *testing.T) {
	// 未读统计走 COUNT(*)：标记已读后 social_read 的 INSERT 把 last_read_id 抬到最大 id。
	stored := int64(0)
	db := &socialFakeDB{me: socialMe(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}},
		valFn: func(q string, _ []any) (any, bool, error) {
			switch {
			case strings.Contains(q, "SELECT COALESCE(MAX(id), 0)"):
				return int64(77), true, nil
			case strings.Contains(q, "SELECT last_read_id FROM social_reads"):
				return stored, true, nil
			}
			return nil, false, nil
		}}
	// INSERT ... ON DUPLICATE KEY UPDATE 的语义：last_read_id 只会变大（GREATEST）。
	db.execFn = func(q string, args []any) (store.ExecResult, error) {
		if strings.Contains(q, "INSERT INTO social_reads") {
			if v := phpInt(args[2]); v > stored {
				stored = v
			}
		}
		return store.ExecResult{RowsAffected: 1}, nil
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_read", `{"token":"t","group_id":5}`)
	if got := dataOf(t, w)["last_read_id"]; got != float64(77) {
		t.Fatalf("last_read_id=%v, 期望 77", got)
	}
}

// ---------- social_recall ----------

func TestSocialRecallTexts(t *testing.T) {
	groupRule := socialRule{match: "SELECT * FROM social_messages WHERE id = ?", rows: nil}

	notFound := &socialFakeDB{me: socialMe(), rules: []socialRule{groupRule}}
	w := callSocial(t, newSocialRouter(notFound, migratedCols(), nil), "social_recall", `{"token":"t","id":9}`)
	assertError(t, w, http.StatusBadRequest, 1, "消息不存在")

	recalled := &socialFakeDB{me: socialMe(), rules: []socialRule{{match: "SELECT * FROM social_messages WHERE id = ?",
		rows: []store.Row{srow("id", 9, "user_id", 7, "is_recalled", 1, "created_at", phpjsonTimeAgo(10))}}}}
	w2 := callSocial(t, newSocialRouter(recalled, migratedCols(), nil), "social_recall", `{"token":"t","id":9}`)
	assertError(t, w2, http.StatusBadRequest, 1, "消息已经撤回了")

	others := &socialFakeDB{me: socialMe(), rules: []socialRule{{match: "SELECT * FROM social_messages WHERE id = ?",
		rows: []store.Row{srow("id", 9, "user_id", 8, "is_recalled", 0, "created_at", phpjsonTimeAgo(10))}}}}
	w3 := callSocial(t, newSocialRouter(others, migratedCols(), nil), "social_recall", `{"token":"t","id":9}`)
	assertError(t, w3, http.StatusBadRequest, 403, "没有权限撤回这条消息")

	old := &socialFakeDB{me: socialMe(), rules: []socialRule{{match: "SELECT * FROM social_messages WHERE id = ?",
		rows: []store.Row{srow("id", 9, "user_id", 7, "is_recalled", 0, "created_at", phpjsonTimeAgo(400))}}}}
	w4 := callSocial(t, newSocialRouter(old, migratedCols(), nil), "social_recall", `{"token":"t","id":9}`)
	assertError(t, w4, http.StatusBadRequest, 1, "只能撤回 5 分钟内的消息")
}

func TestSocialRecallOwnMessageInsideWindow(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), rules: []socialRule{{match: "SELECT * FROM social_messages WHERE id = ?",
		rows: []store.Row{srow("id", 9, "user_id", 7, "is_recalled", 0, "created_at", phpjsonTimeAgo(100))}}}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_recall", `{"token":"t","id":9}`)
	want := `{"code":0,"msg":"ok","data":{"ok":true}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	up := db.execs("UPDATE social_messages SET is_recalled = 1")
	if len(up) != 1 || up[0].Args[0] != int64(7) || up[0].Args[1] != int64(9) {
		t.Fatalf("UPDATE 参数=%v", up)
	}
}

func TestSocialRecallAdminIgnoresWindow(t *testing.T) {
	db := &socialFakeDB{me: socialAdmin(), rules: []socialRule{{match: "SELECT * FROM social_messages WHERE id = ?",
		rows: []store.Row{srow("id", 9, "user_id", 8, "is_recalled", 0, "created_at", phpjsonTimeAgo(4000))}}}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_recall", `{"token":"t","id":9}`)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员必须能撤回过期消息: %s", w.Body.String())
	}
}

// ---------- social_group_join / leave ----------

func TestSocialGroupJoinWritesSystemMessage(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), insertID: 55,
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}},
		valFn: func(q string, _ []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM social_group_members WHERE group_id = ?") {
				return int64(3), true, nil
			}
			return nil, false, nil
		}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_join", `{"token":"t","group_id":5}`)
	want := `{"code":0,"msg":"ok","data":{"group_id":5,"joined":1,"already":0,"member_count":3}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	ins := db.execs("INSERT IGNORE INTO social_group_members")
	if len(ins) != 1 || phpInt(ins[0].Args[0]) != 5 || phpInt(ins[0].Args[1]) != 7 || ins[0].Args[2] != "member" {
		t.Fatalf("成员 INSERT 参数=%v", ins)
	}
	sys := db.execs("INSERT INTO social_messages")
	if len(sys) != 1 || sys[0].Args[2] != "爱丽丝加入了群聊" {
		t.Fatalf("系统消息=%v", sys)
	}
}

func TestSocialGroupJoinIsIdempotent(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), zeroRows: true,
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}},
		valFn: func(q string, _ []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM social_group_members WHERE group_id = ?") {
				return int64(3), true, nil
			}
			return nil, false, nil
		}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_join", `{"token":"t","group_id":5}`)
	want := `{"code":0,"msg":"ok","data":{"group_id":5,"joined":1,"already":1,"member_count":3}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	if len(db.execs("INSERT INTO social_messages")) != 0 {
		t.Fatal("已是成员时不能再写系统消息")
	}
}

func TestSocialGroupJoinAdminBecomesOwner(t *testing.T) {
	db := &socialFakeDB{me: socialAdmin(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}}}
	rt := newSocialRouter(db, migratedCols(), nil)
	callSocial(t, rt, "social_group_join", `{"token":"t","group_id":5}`)
	if got := db.execs("INSERT IGNORE INTO social_group_members")[0].Args[2]; got != "owner" {
		t.Fatalf("管理员入群的成员角色=%v, 期望 owner", got)
	}
}

func TestSocialGroupJoinParamErrors(t *testing.T) {
	db := &socialFakeDB{me: socialMe()}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_join", `{"token":"t","group_id":0}`)
	assertError(t, w, http.StatusBadRequest, 1, "参数错误")

	missing := &socialFakeDB{me: socialMe()}
	rt2 := newSocialRouter(missing, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_group_join", `{"token":"t","group_id":5}`)
	assertError(t, w2, http.StatusBadRequest, 1, "群组不存在或已停用")
}

func TestSocialGroupLeaveWritesSystemMessage(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), rowsAffected: 1,
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}},
		valFn: func(q string, _ []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM social_group_members WHERE group_id = ?") {
				return int64(2), true, nil
			}
			return nil, false, nil
		}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_leave", `{"token":"t","group_id":5}`)
	want := `{"code":0,"msg":"ok","data":{"group_id":5,"left":1,"member_count":2}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	del := db.execs("DELETE FROM social_group_members")
	if len(del) != 1 || phpInt(del[0].Args[0]) != 5 || phpInt(del[0].Args[1]) != 7 {
		t.Fatalf("DELETE 参数=%v", del)
	}
	sys := db.execs("INSERT INTO social_messages")
	if len(sys) != 1 || sys[0].Args[2] != "爱丽丝退出了群聊" {
		t.Fatalf("系统消息=%v", sys)
	}
}

func TestSocialGroupLeaveNotMember(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), zeroRows: true,
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_leave", `{"token":"t","group_id":5}`)
	assertError(t, w, http.StatusBadRequest, 1, "你还不是群成员")
	if len(db.execs("INSERT INTO social_messages")) != 0 {
		t.Fatal("退群失败时不能写系统消息")
	}
}

// ---------- social_group_images ----------

func TestSocialGroupImagesGolden(t *testing.T) {
	img := testS3Public + "/chat/a.jpg"
	db := &socialFakeDB{rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		{match: "FROM social_messages m LEFT JOIN users u ON u.id = m.user_id", rows: []store.Row{
			srow("id", 9, "user_id", 7, "image", img, "image_w", 100, "image_h", 200,
				"created_at", "2026-01-01 10:00:00", "nickname", "爱丽丝", "username", "alice", "avatar", ""),
		}},
	}}
	rt := newSocialRouter(db, migratedCols(), nil)
	db.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_messages m") {
			return int64(2), true, nil
		}
		return nil, false, nil
	}
	w := callSocial(t, rt, "social_group_images", `{"group_id":5}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":9,"user_id":7,"nickname":"爱丽丝","username":"alice","avatar":"","image":"https:\/\/fenglin.cn-nb1.rains3.com\/chat\/a.jpg","image_w":100,"image_h":200,"created_at":"2026-01-01 10:00:00"}],"page":1,"page_size":30,"total":2,"has_more":true}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	sel := db.queries("FROM social_messages m LEFT JOIN users u ON u.id = m.user_id")[0]
	if phpInt(sel.Args[len(sel.Args)-2]) != 30 || phpInt(sel.Args[len(sel.Args)-1]) != 0 {
		t.Fatalf("LIMIT/OFFSET 参数=%v", sel.Args)
	}
}

func TestSocialGroupImagesNicknameFallbackAndPaging(t *testing.T) {
	db := &socialFakeDB{rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		{match: "FROM social_messages m LEFT JOIN users u ON u.id = m.user_id", rows: []store.Row{
			srow("id", 9, "user_id", 8, "image", "x", "image_w", 0, "image_h", 0,
				"created_at", "", "nickname", "", "username", "", "avatar", ""),
		}},
	}}
	db.valFn = func(q string, _ []any) (any, bool, error) { return int64(3), true, nil }
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_images", `{"group_id":5,"page":2,"page_size":60}`)
	if got := dataOf(t, w)["list"].([]any)[0].(map[string]any)["nickname"]; got != "用户8" {
		t.Fatalf("空昵称必须回落成 用户<id>, got=%v", got)
	}
	if got := dataOf(t, w)["page"]; got != float64(2) {
		t.Fatalf("page=%v", got)
	}
	// has_more = (off + len(list)) < total = (60 + 1) < 3 = false
	if got := dataOf(t, w)["has_more"]; got != false {
		t.Fatalf("has_more=%v, 期望 false", got)
	}
}

func TestSocialGroupImagesParamError(t *testing.T) {
	rt := newSocialRouter(&socialFakeDB{}, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_images", `{"group_id":0}`)
	assertError(t, w, http.StatusBadRequest, 1, "参数错误")
}

// ---------- social_mute_set ----------

func TestSocialMuteSetToggle(t *testing.T) {
	db := &socialFakeDB{me: socialMe(),
		rules: []socialRule{{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}}}
	rt := newSocialRouter(db, migratedCols(), nil)

	w := callSocial(t, rt, "social_mute_set", `{"token":"t","group_id":5,"muted":1}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"group_id":5,"muted":1}}` {
		t.Fatalf("开启免打扰响应=%s", w.Body.String())
	}
	ins := db.execs("INSERT IGNORE INTO social_mutes")
	if len(ins) != 1 || phpInt(ins[0].Args[0]) != 5 || phpInt(ins[0].Args[1]) != 7 {
		t.Fatalf("免打扰 INSERT 参数=%v", ins)
	}

	w2 := callSocial(t, rt, "social_mute_set", `{"token":"t","group_id":5,"muted":0}`)
	if w2.Body.String() != `{"code":0,"msg":"ok","data":{"group_id":5,"muted":0}}` {
		t.Fatalf("关闭免打扰响应=%s", w2.Body.String())
	}
	del := db.execs("DELETE FROM social_mutes")
	if len(del) != 1 || phpInt(del[0].Args[0]) != 5 || phpInt(del[0].Args[1]) != 7 {
		t.Fatalf("免打扰 DELETE 参数=%v", del)
	}
}

// ---------- social_group_members ----------

func TestSocialGroupMembersGolden(t *testing.T) {
	member := srow("id", 8, "username", "bob", "nickname", "鲍勃", "avatar", "", "tags", "vip",
		"role", "user", "group_role", "owner", "joined_at", "2026-01-02 03:04:05")
	db := &socialFakeDB{me: socialMe(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		{match: "SELECT u.*, m.role AS group_role, m.joined_at", rows: []store.Row{member}},
		{match: "SELECT 1 FROM social_group_members", rows: []store.Row{srow("1", 1)}},
		{match: "FROM social_user_mutes", rows: []store.Row{
			srow("id", 3, "group_id", 5, "until_at", nil, "reason", "刷屏")}},
	}}
	db.valFn = func(q string, _ []any) (any, bool, error) {
		switch {
		case strings.Contains(q, "SELECT COUNT(*) FROM social_group_members m JOIN"):
			return int64(1), true, nil
		case strings.Contains(q, "SELECT COUNT(*) FROM social_group_members WHERE group_id"):
			return int64(4), true, nil
		}
		return nil, false, nil
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_members", `{"token":"t","group_id":5,"keyword":"bo","page":1,"page_size":30}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":8,"username":"bob","nickname":"鲍勃","avatar":"","tags":["vip"],"role":"owner","group_role":"owner","joined_at":"2026-01-02 03:04:05","global_role":"user","is_admin":0,"muted":1,"mute_left":"永久","mute_reason":"刷屏"}],"page":1,"page_size":30,"total":1,"is_member":1,"member_count":4}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	cnt := db.queries("SELECT COUNT(*) FROM social_group_members m JOIN")[0]
	if len(cnt.Args) != 3 || cnt.Args[1] != "%bo%" || cnt.Args[2] != "%bo%" {
		t.Fatalf("keyword 的 LIKE 参数=%v", cnt.Args)
	}
}

func TestSocialGroupMembersGuestSeesZeroIsMember(t *testing.T) {
	db := &socialFakeDB{me: nil,
		rules: []socialRule{
			{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
			{match: "SELECT u.*, m.role AS group_role, m.joined_at", rows: nil},
		}}
	db.valFn = func(q string, _ []any) (any, bool, error) { return int64(2), true, nil }
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_members", `{"group_id":5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("游客必须能看成员列表: %s", w.Body.String())
	}
	d := dataOf(t, w)
	if d["is_member"] != float64(0) {
		t.Fatalf("游客 is_member=%v, 期望 0", d["is_member"])
	}
	if d["member_count"] != float64(2) {
		t.Fatalf("member_count=%v", d["member_count"])
	}
	if got := w.Body.String(); !strings.Contains(got, `"list":[]`) {
		t.Fatalf("空列表必须输出 []（不是 null）: %s", got)
	}
}

// ---------- social_group_notice_set ----------

func TestSocialGroupNoticeSetGolden(t *testing.T) {
	noticeDB := func(existingNotif int64) *socialFakeDB {
		db := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
			{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
			{match: "SELECT id FROM users WHERE is_active = 1 AND id <> ?", rows: []store.Row{srow("id", 8), srow("id", 10)}},
		}}
		db.valFn = func(q string, _ []any) (any, bool, error) {
			if strings.Contains(q, "FROM notifications WHERE user_id") {
				if existingNotif > 0 {
					return existingNotif, true, nil
				}
				return nil, false, nil
			}
			return nil, false, nil
		}
		return db
	}

	db := noticeDB(0)
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_notice_set", `{"token":"t","group_id":5,"notice":"周五维护"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true,"notified":true}}` {
		t.Fatalf("响应=%s", w.Body.String())
	}
	up := db.execs("UPDATE social_groups SET notice = ?")
	if len(up) != 1 || up[0].Args[0] != "周五维护" || phpInt(up[0].Args[1]) != 5 {
		t.Fatalf("公告 UPDATE 参数=%v", up)
	}
	ins := db.execs("INSERT INTO notifications")
	if len(ins) != 2 {
		t.Fatalf("公告通知条数=%d", len(ins))
	}
	for i, want := range []int64{8, 10} {
		a := ins[i].Args
		if phpInt(a[0]) != want || a[1] != "「测试群」群公告更新" || a[2] != "周五维护" || a[3] != "social" || a[4] != "notice:5" {
			t.Fatalf("第 %d 条公告通知=%v", i, a)
		}
	}

	// 空公告：不通知，notified=false
	db2 := noticeDB(0)
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_group_notice_set", `{"token":"t","group_id":5,"notice":""}`)
	if w2.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true,"notified":false}}` {
		t.Fatalf("空公告响应=%s", w2.Body.String())
	}
	if len(db2.execs("INSERT INTO notifications")) != 0 {
		t.Fatal("空公告不该发通知")
	}
}

// notify_merge 的合并语义：已有同 type+title 的未读通知时只 UPDATE，不再 INSERT。
func TestNotifyMergeUpdatesExistingNotification(t *testing.T) {
	db := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		{match: "SELECT id FROM users WHERE is_active = 1 AND id <> ?", rows: []store.Row{srow("id", 8)}},
	}}
	db.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "FROM notifications WHERE user_id") {
			return int64(66), true, nil
		}
		return nil, false, nil
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	callSocial(t, rt, "social_group_notice_set", `{"token":"t","group_id":5,"notice":"第二次公告"}`)
	if len(db.execs("INSERT INTO notifications")) != 0 {
		t.Fatal("已存在未读通知时不允许再 INSERT（否则用户被刷屏）")
	}
	up := db.execs("UPDATE notifications SET content = ?, link = ?, created_at = NOW()")
	if len(up) != 1 {
		t.Fatalf("UPDATE 次数=%d", len(up))
	}
	a := up[0].Args
	if a[0] != "第二次公告" || a[1] != "notice:5" || phpInt(a[2]) != 66 {
		t.Fatalf("合并 UPDATE 参数=%v", a)
	}
}

func TestSocialGroupNoticeSetTexts(t *testing.T) {
	// 非管理员 -> 403
	notAdmin := &socialFakeDB{me: socialMe()}
	w := callSocial(t, newSocialRouter(notAdmin, migratedCols(), nil), "social_group_notice_set", `{"token":"t","group_id":5,"notice":"x"}`)
	assertError(t, w, http.StatusBadRequest, 403, "没有权限, 仅管理员可操作")

	// 未登录
	w2 := callSocial(t, newSocialRouter(&socialFakeDB{}, migratedCols(), nil), "social_group_notice_set", `{"notice":"x"}`)
	assertError(t, w2, http.StatusUnauthorized, 401, "未登录")

	// 公告超长
	long := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}}}}
	w3 := callSocial(t, newSocialRouter(long, migratedCols(), nil), "social_group_notice_set",
		`{"token":"t","group_id":5,"notice":"`+strings.Repeat("字", 501)+`"}`)
	assertError(t, w3, http.StatusBadRequest, 1, "公告不能超过 500 个字")

	// 群不存在（allowInactive=true，但行不存在仍然报错）
	missing := &socialFakeDB{me: socialAdmin()}
	w4 := callSocial(t, newSocialRouter(missing, migratedCols(), nil), "social_group_notice_set", `{"token":"t","group_id":5,"notice":"x"}`)
	assertError(t, w4, http.StatusBadRequest, 1, "群组不存在或已停用")

	// 停用的群也能改公告（allowInactive=true）
	inactive := socialGroupRow()
	inactive["is_active"] = 0
	inactiveDB := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{inactive}},
		{match: "SELECT id FROM users WHERE is_active = 1 AND id <> ?", rows: nil}}}
	w5 := callSocial(t, newSocialRouter(inactiveDB, migratedCols(), nil), "social_group_notice_set", `{"token":"t","group_id":5,"notice":"停用群公告"}`)
	if w5.Code != http.StatusOK {
		t.Fatalf("停用的群也必须能改公告（PHP 传 allowInactive=true）: %s", w5.Body.String())
	}
}

// ---------- social_group_allmute_set ----------

func TestSocialGroupAllmuteSet(t *testing.T) {
	db := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
		{match: "SELECT id FROM social_groups WHERE id = ?", rows: []store.Row{srow("id", 5)}}}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_allmute_set", `{"token":"t","group_id":5,"muted":1}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"group_id":5,"all_muted":1}}` {
		t.Fatalf("响应=%s", w.Body.String())
	}
	up := db.execs("UPDATE social_groups SET all_muted = ?")
	if len(up) != 1 || phpInt(up[0].Args[0]) != 1 || phpInt(up[0].Args[1]) != 5 {
		t.Fatalf("UPDATE 参数=%v", up)
	}

	// muted 非 1 一律归一化成 0
	w2 := callSocial(t, rt, "social_group_allmute_set", `{"token":"t","group_id":5,"muted":"yes"}`)
	if w2.Body.String() != `{"code":0,"msg":"ok","data":{"group_id":5,"all_muted":0}}` {
		t.Fatalf("muted 归一化失败: %s", w2.Body.String())
	}
}

func TestSocialGroupAllmuteSetTexts(t *testing.T) {
	rt := newSocialRouter(&socialFakeDB{me: socialAdmin()}, migratedCols(), nil)
	w := callSocial(t, rt, "social_group_allmute_set", `{"token":"t","group_id":0}`)
	assertError(t, w, http.StatusBadRequest, 1, "参数错误")

	rt2 := newSocialRouter(&socialFakeDB{me: socialAdmin()}, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_group_allmute_set", `{"token":"t","group_id":9,"muted":1}`)
	assertError(t, w2, http.StatusBadRequest, 1, "群组不存在")

	rt3 := newSocialRouter(&socialFakeDB{me: socialMe()}, migratedCols(), nil)
	w3 := callSocial(t, rt3, "social_group_allmute_set", `{"token":"t","group_id":9,"muted":1}`)
	assertError(t, w3, http.StatusBadRequest, 403, "没有权限, 仅管理员可操作")
}

// ---------- admin_user_mute / admin_user_unmute ----------

func adminMuteDB() *socialFakeDB {
	db := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
		{match: "SELECT id, nickname, username, role FROM users WHERE id = ?",
			rows: []store.Row{srow("id", 8, "nickname", "鲍勃", "username", "bob", "role", "user")}},
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
	}}
	// 群名走 QueryValue（fetchColumn），假实现里对应 valFn。
	db.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "SELECT name FROM social_groups WHERE id = ?") {
			return "测试群", true, nil
		}
		return nil, false, nil
	}
	return db
}

func TestAdminUserMutePermanentGlobal(t *testing.T) {
	db := adminMuteDB()
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "admin_user_mute", `{"token":"t","user_id":8,"group_id":0,"minutes":0,"reason":"刷屏"}`)
	want := `{"code":0,"msg":"ok","data":{"user_id":8,"group_id":0,"until_at":"","left_text":"永久","reason":"刷屏"}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	ins := db.execs("INSERT INTO social_user_mutes")
	if len(ins) != 1 || !strings.Contains(ins[0].Query, "ON DUPLICATE KEY UPDATE until_at = VALUES(until_at)") {
		t.Fatalf("禁言 INSERT 语句=%v", ins)
	}
	if ins[0].Args[2] != nil {
		t.Fatalf("永久禁言的 until_at 必须是 NULL, got=%v", ins[0].Args[2])
	}
	if phpInt(ins[0].Args[4]) != 1 { // created_by = 管理员 id
		t.Fatalf("created_by=%v", ins[0].Args[4])
	}
	notif := db.execs("INSERT INTO notifications")
	if len(notif) != 1 || notif[0].Args[1] != "你已被禁言" ||
		notif[0].Args[2] != "全站被管理员禁言, 永久, 原因: 刷屏" || notif[0].Args[3] != "admin" {
		t.Fatalf("禁言通知=%v", notif)
	}
}

func TestAdminUserMuteInGroupOneHour(t *testing.T) {
	db := adminMuteDB()
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "admin_user_mute", `{"token":"t","user_id":8,"group_id":5,"minutes":60}`)
	d := dataOf(t, w)
	if d["left_text"] != "剩余 1 小时" || d["until_at"] == "" {
		t.Fatalf("响应=%s", w.Body.String())
	}
	until := d["until_at"].(string)
	ts, ok := phpStrtotime(until)
	if !ok || ts < nowUnix()+3590 || ts > nowUnix()+3610 {
		t.Fatalf("until_at=%s (应为 now+60 分钟)", until)
	}
	notif := db.execs("INSERT INTO notifications")
	if got := notif[0].Args[2]; got != "在「测试群」被管理员禁言, 剩余 1 小时" {
		t.Fatalf("禁言通知正文=%v", got)
	}
}

func TestAdminUserMuteClampAndTiers(t *testing.T) {
	// minutes 上限 432000（300 天）
	db := adminMuteDB()
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "admin_user_mute", `{"token":"t","user_id":8,"minutes":9999999}`)
	if got := dataOf(t, w)["left_text"]; got != "剩余 300 天" {
		t.Fatalf("left_text=%v, 期望 剩余 300 天", got)
	}
	// 负数按 0（永久）处理
	db2 := adminMuteDB()
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "admin_user_mute", `{"token":"t","user_id":8,"minutes":-5}`)
	if got := dataOf(t, w2)["left_text"]; got != "永久" {
		t.Fatalf("left_text=%v, 期望 永久", got)
	}
	// 分钟档：ceil，最小 1
	db3 := adminMuteDB()
	rt3 := newSocialRouter(db3, migratedCols(), nil)
	w3 := callSocial(t, rt3, "admin_user_mute", `{"token":"t","user_id":8,"minutes":1}`)
	if got := dataOf(t, w3)["left_text"]; got != "剩余 1 分钟" {
		t.Fatalf("left_text=%v, 期望 剩余 1 分钟", got)
	}
}

func TestAdminUserMuteTexts(t *testing.T) {
	rt := newSocialRouter(&socialFakeDB{me: socialAdmin()}, migratedCols(), nil)
	w := callSocial(t, rt, "admin_user_mute", `{"token":"t","user_id":8,"reason":"`+strings.Repeat("字", 61)+`"}`)
	assertError(t, w, http.StatusBadRequest, 1, "禁言原因不能超过 60 个字")

	rtNonAdmin := newSocialRouter(&socialFakeDB{me: socialMe()}, migratedCols(), nil)
	wNon := callSocial(t, rtNonAdmin, "admin_user_mute", `{"token":"t","user_id":8}`)
	assertError(t, wNon, http.StatusBadRequest, 403, "没有权限, 仅管理员可操作")

	missingUser := &socialFakeDB{me: socialAdmin()}
	rtMissing := newSocialRouter(missingUser, migratedCols(), nil)
	w2 := callSocial(t, rtMissing, "admin_user_mute", `{"token":"t","user_id":8}`)
	assertError(t, w2, http.StatusBadRequest, 1, "用户不存在")

	adminTarget := &socialFakeDB{me: socialAdmin(), rules: []socialRule{
		{match: "SELECT id, nickname, username, role FROM users WHERE id = ?",
			rows: []store.Row{srow("id", 1, "role", "admin")}}}}
	rtTarget := newSocialRouter(adminTarget, migratedCols(), nil)
	w3 := callSocial(t, rtTarget, "admin_user_mute", `{"token":"t","user_id":1}`)
	assertError(t, w3, http.StatusBadRequest, 1, "不能禁言管理员")
}

func TestAdminUserUnmute(t *testing.T) {
	db := &socialFakeDB{me: socialAdmin(), rowsAffected: 1}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "admin_user_unmute", `{"token":"t","user_id":8,"group_id":5}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"user_id":8,"group_id":5,"removed":1}}` {
		t.Fatalf("响应=%s", w.Body.String())
	}
	del := db.execs("DELETE FROM social_user_mutes")
	if len(del) != 1 || phpInt(del[0].Args[0]) != 8 || phpInt(del[0].Args[1]) != 5 {
		t.Fatalf("DELETE 参数=%v", del)
	}
	notif := db.execs("INSERT INTO notifications")
	if len(notif) != 1 || notif[0].Args[1] != "禁言已解除" ||
		notif[0].Args[2] != "管理员已解除你的禁言, 现在可以正常发言了" || notif[0].Args[3] != "admin" {
		t.Fatalf("解禁通知=%v", notif)
	}

	// 没有删到行 -> 不发通知
	db2 := &socialFakeDB{me: socialAdmin(), zeroRows: true}
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "admin_user_unmute", `{"token":"t","user_id":8,"group_id":5}`)
	if w2.Body.String() != `{"code":0,"msg":"ok","data":{"user_id":8,"group_id":5,"removed":0}}` {
		t.Fatalf("响应=%s", w2.Body.String())
	}
	if len(db2.execs("INSERT INTO notifications")) != 0 {
		t.Fatal("removed=0 时不该发通知")
	}

	// uid <= 0 -> 参数错误
	rt3 := newSocialRouter(&socialFakeDB{me: socialAdmin()}, migratedCols(), nil)
	w3 := callSocial(t, rt3, "admin_user_unmute", `{"token":"t","user_id":0}`)
	assertError(t, w3, http.StatusBadRequest, 1, "参数错误")
}

// ---------- social_groups / social_group golden ----------

func groupsDB() *socialFakeDB {
	g := socialGroupRow()
	g["message_count"] = 7
	db := &socialFakeDB{me: socialMe(), rules: []socialRule{
		{match: "FROM social_groups g WHERE g.is_active = 1", rows: []store.Row{g}},
		{match: "SELECT group_id FROM social_mutes WHERE user_id = ?", rows: []store.Row{srow("group_id", 5)}},
		{match: "COUNT(*) AS c", rows: []store.Row{srow("group_id", 5, "c", 2, "first_id", 11)}},
		{match: "m.at_users", rows: []store.Row{srow("id", 12, "group_id", 5, "at_users", "7")}},
		{match: "AND m.group_id IN", rows: []store.Row{srow("id", 12, "group_id", 5, "user_id", 8,
			"content", "hi", "image", "", "msg_type", "", "created_at", "2026-01-03 10:00:00",
			"nickname", "鲍勃", "username", "bob")}},
		{match: "SELECT 1 FROM social_group_members", rows: []store.Row{srow("1", 1)}},
	}}
	db.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_group_members WHERE group_id = ?") {
			return int64(4), true, nil
		}
		return nil, false, nil
	}
	return db
}

func TestSocialGroupsGolden(t *testing.T) {
	rt := newSocialRouter(groupsDB(), migratedCols(), nil)
	w := callSocial(t, rt, "social_groups", `{"token":"t"}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"muted":1,"unread":2,"first_unread_id":11,` +
		`"last_message":{"id":12,"user_id":8,"nickname":"鲍勃","content":"hi","image":"","msg_type":"","video":"","video_w":0,"video_h":0,"video_duration":0,"created_at":"2026-01-03 10:00:00"},` +
		`"last_time":"2026-01-03 10:00:00","at_me":1,"at_me_first":12,"at_all":0,"at_all_first":0,` +
		`"id":5,"name":"测试群","icon":"","description":"","notice":"","member_count":4,"is_member":1,` +
		`"message_count":7,"sort_order":0,"is_active":1,"all_muted":0,"created_at":"2026-01-01 10:00:00"}]}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

// 游客看群列表：未登录时 muted/unread/at_me 全 0，is_member 0。
func TestSocialGroupsGuest(t *testing.T) {
	db := groupsDB()
	db.me = nil
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_groups", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("游客必须能看群列表: %s", w.Body.String())
	}
	if n := len(db.queries("FROM social_mutes")); n != 0 {
		t.Fatalf("未登录不该查免打扰, 次数=%d", n)
	}
	if n := len(db.queries("LEFT JOIN social_reads")); n != 0 {
		t.Fatalf("未登录不该查未读/@, 次数=%d", n)
	}
	if got := dataOf(t, w)["list"].([]any)[0].(map[string]any)["is_member"]; got != float64(0) {
		t.Fatalf("游客 is_member=%v", got)
	}
}

func TestSocialGroupGolden(t *testing.T) {
	db := groupsDB()
	// social_group 用的是 social_group_or_404 的 SELECT *（没有 message_count 列 -> 0）
	db.rules = append([]socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		{match: "SELECT DISTINCT u.id, u.nickname, u.username", rows: []store.Row{
			srow("id", 1, "nickname", "管理员", "username", "root")}},
	}, db.rules...)
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_group", `{"token":"t","id":5}`)
	want := `{"code":0,"msg":"ok","data":{"group":{"muted":1,"unread":2,"first_unread_id":11,` +
		`"last_message":{"id":12,"user_id":8,"nickname":"鲍勃","content":"hi","image":"","msg_type":"","video":"","video_w":0,"video_h":0,"video_duration":0,"created_at":"2026-01-03 10:00:00"},` +
		`"last_time":"2026-01-03 10:00:00","at_me":1,"at_me_first":12,"at_all":0,"at_all_first":0,` +
		`"id":5,"name":"测试群","icon":"","description":"","notice":"","member_count":4,"is_member":1,` +
		`"message_count":0,"sort_order":0,"is_active":1,"all_muted":0,"created_at":"2026-01-01 10:00:00"},` +
		`"notice":"","admins":[{"id":"1","nickname":"管理员","username":"root"}]}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// PHP 死查询（结果被 social_group_public 覆盖）也必须照发。
	if n := len(db.queries("SELECT COUNT(DISTINCT user_id) AS c")); n != 1 {
		t.Fatalf("PHP 死查询次数=%d, 期望 1（照抄不修）", n)
	}
}

// ---------- social_messages golden ----------

func TestSocialMessagesGoldenWithQuote(t *testing.T) {
	msg := srow("id", 21, "group_id", 5, "user_id", 8, "content", "引用回复", "image", "", "image_w", 0,
		"image_h", 0, "at_users", "7", "quote_id", 20, "is_recalled", 0,
		"created_at", "2026-01-03 10:00:00", "nickname", "鲍勃", "username", "bob", "avatar", "", "role", "user", "tags", "")
	db := &socialFakeDB{me: socialMe(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		// 被引用的原消息：这个查询同样含 "FROM social_messages m LEFT JOIN users"，所以必须排在消息分页规则前面。
		{match: "SELECT m.id, m.content, m.user_id, u.nickname", rows: []store.Row{
			srow("id", 20, "content", "原消息", "user_id", 7, "nickname", "爱丽丝")}},
		{match: "SELECT m.*, u.nickname, u.username, u.avatar", rows: []store.Row{msg}},
		{match: "SELECT id, nickname, username FROM users WHERE id IN", rows: []store.Row{
			srow("id", 8, "nickname", "鲍勃", "username", "bob")}},
	}}
	db.valFn = func(q string, _ []any) (any, bool, error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM social_messages WHERE group_id = ? AND id < ?") {
			return int64(1), true, nil
		}
		return nil, false, nil
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_messages", `{"token":"t","group_id":5,"limit":1}`)
	want := `{"code":0,"msg":"ok","data":{"list":[{"id":21,"group_id":5,"group_name":"","user_id":8,"nickname":"鲍勃","avatar":"","role":"user","tags":[],"msg_type":"","content":"引用回复","image":"","image_w":0,"image_h":0,"video":"","video_w":0,"video_h":0,"video_duration":0,"video_size":0,"at":[7],"quote_id":20,"quote_nickname":"爱丽丝","quote_content":"原消息","is_recalled":0,"created_at":"2026-01-03 10:00:00","time_text":"10:00"}],"has_more":true,"has_more_before":true,"unread":0,"first_unread_id":0,"my_id":7}}`
	if w.Body.String() != want {
		t.Fatalf("响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// PHP 死查询（at_names 不输出）也必须照发。
	if n := len(db.queries("SELECT id, nickname, username FROM users WHERE id IN")); n != 1 {
		t.Fatalf("at_names 死查询次数=%d, 期望 1", n)
	}
}

// at_users 尾随逗号在 social_msg_public 的 at 字段里会留下一个 0（PHP 也不过滤）。
func TestSocialMsgPublicKeepsTrailingZeroInAt(t *testing.T) {
	rt := newSocialRouter(&socialFakeDB{}, migratedCols(), nil)
	_ = rt
	o := socialMsgPublic(srow("id", 1, "group_id", 5, "user_id", 8, "content", "x", "at_users", "7,",
		"created_at", "2026-01-03 10:00:00"))
	v, _ := o.Get("at")
	list, ok := v.([]any)
	if !ok || len(list) != 2 || list[0] != int64(7) || list[1] != int64(0) {
		t.Fatalf("at=%v, 期望 [7 0]", v)
	}
}

func TestSocialMessagesAroundOrdering(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
	}}
	// 命中顺序：<= around 走 older，> around 走 newer（都是 DESC/ASC 各一次）。
	db.allFn = func(q string, _ []any) ([]store.Row, error) {
		switch {
		case strings.Contains(q, "m.id <= ? ORDER BY m.id DESC"):
			return []store.Row{srow("id", 3), srow("id", 1)}, nil // DESC 后要 reverse
		case strings.Contains(q, "m.id > ? ORDER BY m.id ASC"):
			return []store.Row{srow("id", 4), srow("id", 5)}, nil
		case strings.Contains(q, "SELECT id, nickname, username FROM users WHERE id IN"):
			return nil, nil
		}
		return nil, nil
	}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_messages", `{"token":"t","group_id":5,"around_id":2,"limit":10}`)
	list := dataOf(t, w)["list"].([]any)
	var ids []float64
	for _, it := range list {
		ids = append(ids, it.(map[string]any)["id"].(float64))
	}
	if len(ids) != 4 || ids[0] != 1 || ids[1] != 3 || ids[2] != 4 || ids[3] != 5 {
		t.Fatalf("around 分页顺序=%v, 期望 [1 3 4 5]（前一半反转为正序，再接后一半）", ids)
	}
	// half = max(5, floor(10/2)) = 5
	q := db.queries("m.id <= ? ORDER BY m.id DESC")[0]
	if phpInt(q.Args[len(q.Args)-1]) != 5 {
		t.Fatalf("half 参数=%v, 期望 5", q.Args)
	}
}

func TestSocialMessagesEmptyPage(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), rules: []socialRule{
		{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
	}}
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_messages", `{"token":"t","group_id":5}`)
	d := dataOf(t, w)
	if got := w.Body.String(); !strings.Contains(got, `"list":[]`) {
		t.Fatalf("空列表必须输出 []: %s", got)
	}
	if d["has_more"] != false || d["has_more_before"] != false {
		t.Fatalf("空页 has_more/has_more_before 必须为 false: %v", d)
	}
	if n := len(db.queries("SELECT COUNT(*) FROM social_messages WHERE group_id = ? AND id < ?")); n != 0 {
		t.Fatal("没有消息时不该查 has_more_before")
	}
}

// ---------- 路由注册 ----------

// 15 个 action 必须全部走 Go 原生（透传会因为没有 PHP-FPM 而失败/输出非 JSON 壳）。
func TestSocialRoutesAreNative(t *testing.T) {
	actions := []string{
		"social_groups", "social_group", "social_messages", "social_read", "social_send",
		"social_recall", "social_group_notice_set", "social_group_allmute_set",
		"social_group_join", "social_group_leave", "social_group_images", "social_mute_set",
		"social_group_members", "admin_user_mute", "admin_user_unmute",
	}
	rt := newSocialRouter(&socialFakeDB{}, migratedCols(), nil)
	for _, a := range actions {
		w := callSocial(t, rt, a, `{}`)
		body := w.Body.String()
		if !strings.HasPrefix(body, `{"code":`) {
			t.Fatalf("%s 的响应不是 Go 原生 JSON 壳: %s", a, body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("%s 的 Content-Type=%q", a, ct)
		}
	}
}

// before/after 分页：before 先倒序取一页再反转成正序；after 直接正序返回。
func TestSocialMessagesBeforeAndAfterPaging(t *testing.T) {
	newDB := func() (*socialFakeDB, *[]string) {
		var seen []string
		db := &socialFakeDB{me: socialMe(), rules: []socialRule{
			{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		}}
		db.allFn = func(q string, args []any) ([]store.Row, error) {
			switch {
			case strings.Contains(q, "m.id < ? ORDER BY m.id DESC"):
				seen = append(seen, "before")
				return []store.Row{srow("id", 7), srow("id", 5)}, nil
			case strings.Contains(q, "m.id > ? ORDER BY m.id ASC"):
				seen = append(seen, "after")
				return []store.Row{srow("id", 8), srow("id", 9)}, nil
			}
			return nil, nil
		}
		return db, &seen
	}
	ids := func(w *httptest.ResponseRecorder) []float64 {
		list := dataOf(t, w)["list"].([]any)
		var out []float64
		for _, it := range list {
			out = append(out, it.(map[string]any)["id"].(float64))
		}
		return out
	}

	db, seen := newDB()
	rt := newSocialRouter(db, migratedCols(), nil)
	w := callSocial(t, rt, "social_messages", `{"token":"t","group_id":5,"before_id":10}`)
	got := ids(w)
	if len(got) != 2 || got[0] != 5 || got[1] != 7 {
		t.Fatalf("before 分页顺序=%v, 期望 [5 7]（倒序取一页后反转）", got)
	}
	if len(*seen) != 1 || (*seen)[0] != "before" {
		t.Fatalf("走了 %v, 期望只有 before", *seen)
	}

	db2, seen2 := newDB()
	rt2 := newSocialRouter(db2, migratedCols(), nil)
	w2 := callSocial(t, rt2, "social_messages", `{"token":"t","group_id":5,"after_id":7}`)
	got2 := ids(w2)
	if len(got2) != 2 || got2[0] != 8 || got2[1] != 9 {
		t.Fatalf("after 分页顺序=%v, 期望 [8 9]", got2)
	}
	if len(*seen2) != 1 || (*seen2)[0] != "after" {
		t.Fatalf("走了 %v, 期望只有 after", *seen2)
	}
}

// limit 的钳制：min(50, max(1, ...))。
func TestSocialMessagesLimitClamp(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int64
	}{
		{`{"token":"t","group_id":5,"limit":999}`, 50},
		{`{"token":"t","group_id":5,"limit":0}`, 1},
		{`{"token":"t","group_id":5,"limit":-3}`, 1},
		{`{"token":"t","group_id":5}`, 30},
	} {
		db := &socialFakeDB{me: socialMe(), rules: []socialRule{
			{match: "SELECT * FROM social_groups WHERE id = ?", rows: []store.Row{socialGroupRow()}},
		}}
		rt := newSocialRouter(db, migratedCols(), nil)
		callSocial(t, rt, "social_messages", tc.body)
		q := db.queries("ORDER BY m.id DESC LIMIT ?")
		if len(q) != 1 {
			t.Fatalf("默认分支查询次数=%d (%s)", len(q), tc.body)
		}
		if got := phpInt(q[0].Args[len(q[0].Args)-1]); got != tc.want {
			t.Fatalf("limit=%d, 期望 %d (%s)", got, tc.want, tc.body)
		}
	}
}
