package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5C 单测：全部假 DB（不连 MySQL/Redis），逐字节断言 JSON 与 HTTP 状态码。
const (
	contentUserToken  = "tok-content-user"
	contentAdminToken = "tok-content-admin"
)

// ---------- 假数据访问层 ----------

type contentFake struct {
	allFn  func(q string, args []any) ([]store.Row, error)
	rowFn  func(q string, args []any) (store.Row, error)
	valFn  func(q string, args []any) (any, bool, error)
	execFn func(q string, args []any) (store.ExecResult, error)
	me     store.Row
	nextID int64
	calls  []fakeCall
}

func newContentFake() *contentFake { return &contentFake{me: socialAdmin(), nextID: 55} }

func (f *contentFake) log(method, q string, args []any) {
	f.calls = append(f.calls, fakeCall{Method: method, Query: q, Args: args})
}

func (f *contentFake) CurrentUserRow(_ context.Context, token string) (store.Row, error) {
	f.log("CurrentUserRow", "", []any{token})
	switch token {
	case "":
		return nil, nil
	case contentUserToken:
		return socialMe(), nil
	}
	return f.me, nil
}

func (f *contentFake) QueryRow(_ context.Context, q string, args ...any) (store.Row, error) {
	f.log("QueryRow", q, args)
	if f.rowFn == nil {
		return nil, nil
	}
	return f.rowFn(q, args)
}

func (f *contentFake) QueryAll(_ context.Context, q string, args ...any) ([]store.Row, error) {
	f.log("QueryAll", q, args)
	if f.allFn == nil {
		return nil, nil
	}
	return f.allFn(q, args)
}

func (f *contentFake) QueryValue(_ context.Context, q string, args ...any) (any, bool, error) {
	f.log("QueryValue", q, args)
	if f.valFn == nil {
		return nil, false, nil
	}
	return f.valFn(q, args)
}

func (f *contentFake) Exec(_ context.Context, q string, args ...any) (store.ExecResult, error) {
	f.log("Exec", q, args)
	if f.execFn != nil {
		return f.execFn(q, args)
	}
	return store.ExecResult{LastInsertID: f.nextID, RowsAffected: 1}, nil
}

func (f *contentFake) find(method, match string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

func newContentRouter(f *contentFake) *Router {
	return New(Env{Cfg: config.Load(), Log: testLogger(), Accounts: f})
}

func callContent(rt *Router, action, body string, headers map[string]string) *httptest.ResponseRecorder {
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json", headers)
}

func callContentURL(rt *Router, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	return callAction(rt, "POST", target, body, "application/json", headers)
}

func adminHdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + contentAdminToken}
}
func userHdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + contentUserToken}
}

// ---------- 公开接口：键序与类型 ----------

func contentAppsRow() store.Row {
	return store.Row{
		"id": int64(3), "category_id": int64(2), "name": "测试软件", "icon": "https://x/y.png",
		"version": "1.2.3", "release_date": "2026-09-01", "is_new": int64(1),
		"new_until": "2099-01-01 00:00:00", "description": "描述",
		"screenshots": `["https://a/1.jpg","https://a/2.jpg"]`, "contributor_qq": "12345",
		"package_name": "com.x.y", "download_count": int64(42), "rating": "4.5",
		"sort_order": int64(1), "pack_id": nil, "is_top": int64(0), "is_featured": int64(1),
		"is_active": int64(1), "created_at": "2026-08-01 10:00:00", "updated_at": "2026-08-02 10:00:00",
		"category_name": "工具", "category_color": "#4C6FFF",
	}
}

const contentAppsWantNonAdmin = `{"code":0,"msg":"ok","data":[{"id":3,"category_id":2,"name":"测试软件","icon":"https:\/\/x\/y.png","version":"1.2.3","release_date":"2026-09-01","is_new":1,"new_until":"2099-01-01 00:00:00","description":"描述","screenshots":["https:\/\/a\/1.jpg","https:\/\/a\/2.jpg"],"package_name":"com.x.y","download_count":42,"rating":4.5,"sort_order":1,"pack_id":null,"is_top":0,"is_featured":1,"is_active":1,"created_at":"2026-08-01 10:00:00","updated_at":"2026-08-02 10:00:00","category_name":"工具","category_color":"#4C6FFF"}]}`

func TestContentAppsNonAdminHidesQQAndKeepsKeyOrder(t *testing.T) {
	f := newContentFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM apps a") {
			return []store.Row{contentAppsRow()}, nil
		}
		return nil, nil
	}
	w := callContent(newContentRouter(f), "apps", "", nil)
	if w.Body.String() != contentAppsWantNonAdmin {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), contentAppsWantNonAdmin)
	}
	if strings.Contains(w.Body.String(), `"contributor_qq"`) {
		t.Fatalf("非管理员必须整个键消失: %s", w.Body.String())
	}
	assertCORS(t, w.Header())
	// 访客强制只看上架。
	sqls := f.find("QueryAll", "FROM apps a")
	if len(sqls) != 1 || !strings.Contains(sqls[0].Query, "WHERE a.is_active = 1") {
		t.Fatalf("访客 WHERE 子句不对: %+v", sqls)
	}
}

func TestContentAppsAdminSeesQQ(t *testing.T) {
	f := newContentFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM apps a") {
			return []store.Row{contentAppsRow()}, nil
		}
		return nil, nil
	}
	w := callContent(newContentRouter(f), "apps", `{"include_inactive":1}`, adminHdr())
	want := strings.Replace(contentAppsWantNonAdmin, `"screenshots":["https:\/\/a\/1.jpg","https:\/\/a\/2.jpg"],`,
		`"screenshots":["https:\/\/a\/1.jpg","https:\/\/a\/2.jpg"],"contributor_qq":"12345",`, 1)
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	sqls := f.find("QueryAll", "FROM apps a")
	if len(sqls) != 1 || !strings.Contains(sqls[0].Query, "WHERE 1 = 1") {
		t.Fatalf("管理员 include_inactive 未生效: %+v", sqls)
	}
}

// include_inactive 对普通用户静默忽略（不报 403）。
func TestContentAppsIncludeInactiveIgnoredForUser(t *testing.T) {
	f := newContentFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) { return nil, nil }
	w := callContent(newContentRouter(f), "apps", `{"include_inactive":1}`, userHdr())
	if w.Code != http.StatusOK {
		t.Fatalf("普通用户不应报错: %d %s", w.Code, w.Body.String())
	}
	sqls := f.find("QueryAll", "FROM apps a")
	if len(sqls) != 1 || !strings.Contains(sqls[0].Query, "WHERE a.is_active = 1") {
		t.Fatalf("普通用户的 include_inactive 必须被忽略: %+v", sqls)
	}
	if w.Body.String() != `{"code":0,"msg":"ok","data":[]}` {
		t.Fatalf("空列表应为 []: %s", w.Body.String())
	}
}

func TestContentAppsFilterBranches(t *testing.T) {
	// 有子分类：IN (4,5,3)，不参数化。
	f := newContentFake()
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "SELECT id FROM categories WHERE parent_id") {
			return []store.Row{srow("id", int64(4)), srow("id", int64(5))}, nil
		}
		return nil, nil
	}
	callContent(newContentRouter(f), "apps", `{"category_id":3}`, nil)
	sqls := f.find("QueryAll", "FROM apps a")
	if !strings.Contains(sqls[0].Query, "AND a.category_id IN (4,5,3)") {
		t.Fatalf("父分类展开不对: %s", sqls[0].Query)
	}

	// 无子分类：参数化 = ?。
	f2 := newContentFake()
	f2.allFn = func(q string, args []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM categories WHERE parent_id") {
			return nil, nil
		}
		return nil, nil
	}
	callContent(newContentRouter(f2), "apps", `{"category_id":3}`, nil)
	s2 := f2.find("QueryAll", "FROM apps a")[0]
	if !strings.Contains(s2.Query, "AND a.category_id = ?") || len(s2.Args) != 1 || phpInt(s2.Args[0]) != 3 {
		t.Fatalf("子分类过滤不对: %s args=%v", s2.Query, s2.Args)
	}

	// pack_id=0：走整合包分支，且不再走分类过滤（即使同时传 category_id=3）。
	f3 := newContentFake()
	f3.allFn = func(string, []any) ([]store.Row, error) { return nil, nil }
	callContent(newContentRouter(f3), "apps", `{"pack_id":0,"category_id":3}`, nil)
	s3 := f3.find("QueryAll", "FROM apps a")[0]
	if !strings.Contains(s3.Query, "AND a.pack_id = ?") || strings.Contains(s3.Query, "AND a.category_id") {
		t.Fatalf("pack_id 分支不对: %s", s3.Query)
	}
	if len(s3.Args) != 1 || phpInt(s3.Args[0]) != 0 {
		t.Fatalf("pack_id=0 参数不对: %v", s3.Args)
	}

	// keyword：%词%（不转义 %/_）。
	f4 := newContentFake()
	f4.allFn = func(string, []any) ([]store.Row, error) { return nil, nil }
	callContent(newContentRouter(f4), "apps", `{"keyword":"a%b"}`, nil)
	s4 := f4.find("QueryAll", "FROM apps a")[0]
	if len(s4.Args) != 1 || phpStr(s4.Args[0]) != "%a%b%" {
		t.Fatalf("keyword 参数不对: %v", s4.Args)
	}
}

func TestContentAppDetailParentPackFalseAndAppendedKeys(t *testing.T) {
	f := newContentFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		switch {
		case strings.Contains(q, "FROM apps a"):
			return store.Row{
				"id": int64(7), "category_id": int64(1), "name": "整合包", "icon": "", "version": "2.0",
				"release_date": nil, "is_new": int64(0), "new_until": nil, "description": nil,
				"screenshots": nil, "contributor_qq": "", "package_name": "", "download_count": "5",
				"rating": "3.0", "sort_order": int64(0), "pack_id": int64(5), "is_top": int64(0),
				"is_featured": int64(0), "is_active": int64(1), "created_at": "2026-01-01 00:00:00",
				"updated_at": "2026-01-02 00:00:00", "category_name": "合集",
			}, nil
		case strings.Contains(q, "SELECT id, name, icon FROM apps WHERE id = ?"):
			return nil, nil // 父行已被删 => parent_pack 必须是 false
		}
		return nil, nil
	}
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		switch {
		case strings.Contains(q, "FROM pan_links WHERE app_id"):
			return []store.Row{srow("id", int64(2), "pan_type", "uc", "label", "UC网盘",
				"url", "https://pan.uc/a", "password", "p")}, nil
		case strings.Contains(q, "FROM apps WHERE pack_id"):
			return []store.Row{srow("id", int64(9), "name", "子项", "icon", "", "version", "1.0",
				"download_count", "7")}, nil
		}
		return nil, nil
	}
	w := callContent(newContentRouter(f), "app_detail", `{"id":7}`, adminHdr())
	want := `{"code":0,"msg":"ok","data":{"id":7,"category_id":1,"name":"整合包","icon":"","version":"2.0",` +
		`"release_date":"","is_new":0,"new_until":null,"description":null,"screenshots":[],` +
		`"contributor_qq":"","package_name":"","download_count":5,"rating":3.0,"sort_order":0,` +
		`"pack_id":5,"is_top":0,"is_featured":0,"is_active":1,"created_at":"2026-01-01 00:00:00",` +
		`"updated_at":"2026-01-02 00:00:00","category_name":"合集",` +
		`"pan_links":[{"id":2,"pan_type":"uc","label":"UC网盘","url":"https:\/\/pan.uc\/a","password":"p"}],` +
		`"pack_items":[{"id":9,"name":"子项","icon":"","version":"1.0","download_count":7}],` +
		`"parent_pack":false}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	assertCORS(t, w.Header())
}

func TestContentAppDetailNotFound(t *testing.T) {
	f := newContentFake()
	f.rowFn = func(string, []any) (store.Row, error) { return nil, nil }
	w := callContent(newContentRouter(f), "app_detail", `{"id":999}`, nil)
	assertError(t, w, http.StatusBadRequest, 1, "软件不存在")
}

func TestContentCategoriesNoIntCastAndOrder(t *testing.T) {
	f := newContentFake()
	f.allFn = func(string, []any) ([]store.Row, error) {
		return []store.Row{srow("id", "1", "parent_id", "0", "name", "工具", "color", "#4C6FFF",
			"icon", "https://i/x.png", "sort_order", "3", "created_at", "2026-01-01 00:00:00")}, nil
	}
	w := callContent(newContentRouter(f), "categories", "", nil)
	want := `{"code":0,"msg":"ok","data":[{"id":"1","parent_id":"0","name":"工具","color":"#4C6FFF","icon":"https:\/\/i\/x.png","sort_order":"3","created_at":"2026-01-01 00:00:00"}]}`
	if w.Body.String() != want {
		t.Fatalf("categories 不做 int 强转 + 键序\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

func TestContentBannersAppIDNullAndOrder(t *testing.T) {
	f := newContentFake()
	f.allFn = func(string, []any) ([]store.Row, error) {
		return []store.Row{
			srow("id", int64(1), "image", "https://i/b.png", "title", "横幅", "app_id", nil,
				"url", "https://u/x", "sort_order", int64(2), "is_active", int64(1),
				"created_at", "2026-01-01 00:00:00", "app_name", nil),
			srow("id", "2", "image", "i2", "title", "带应用", "app_id", "9",
				"url", "", "sort_order", "1", "is_active", "1",
				"created_at", "2026-01-02 00:00:00", "app_name", "某应用"),
		}, nil
	}
	w := callContent(newContentRouter(f), "banners", "", nil)
	want := `{"code":0,"msg":"ok","data":[` +
		`{"id":1,"image":"https:\/\/i\/b.png","title":"横幅","app_id":null,"url":"https:\/\/u\/x","sort_order":2,"is_active":1,"created_at":"2026-01-01 00:00:00","app_name":null},` +
		`{"id":2,"image":"i2","title":"带应用","app_id":9,"url":"","sort_order":"1","is_active":"1","created_at":"2026-01-02 00:00:00","app_name":"某应用"}]}`
	if w.Body.String() != want {
		t.Fatalf("banners 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

// ---------- contributors：脱敏与顺序 ----------

func contributorAggRows() []store.Row {
	return []store.Row{
		srow("id", int64(10), "qq", " 12345 ", "name", "甲", "bio", "b1", "sort_order", int64(1), "app_name", "应用A"),
		srow("id", int64(11), "qq", "12345", "name", nil, "bio", nil, "sort_order", int64(2), "app_name", nil),
		srow("id", int64(12), "qq", "   ", "name", "空", "bio", "", "sort_order", int64(3), "app_name", "应用B"),
		srow("id", int64(13), "qq", "67890", "name", "乙", "bio", "b2", "sort_order", int64(4), "app_name", "应用C"),
	}
}

func TestContentContributorsPublicNoQQ(t *testing.T) {
	f := newContentFake()
	f.allFn = func(string, []any) ([]store.Row, error) { return contributorAggRows(), nil }
	w := callContent(newContentRouter(f), "contributors", "", nil)
	want := `{"code":0,"msg":"ok","data":[` +
		`{"id":10,"avatar":"https:\/\/q1.qlogo.cn\/g?b=qq&nk=12345&s=100","name":"甲","bio":"b1","app_names":["应用A"]},` +
		`{"id":13,"avatar":"https:\/\/q1.qlogo.cn\/g?b=qq&nk=67890&s=100","name":"乙","bio":"b2","app_names":["应用C"]}]}`
	if w.Body.String() != want {
		t.Fatalf("contributors 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	if strings.Contains(w.Body.String(), `"qq":`) || strings.Contains(w.Body.String(), `"sort_order":`) {
		t.Fatalf("公开版绝不能出现 qq/sort_order: %s", w.Body.String())
	}
}

func TestContentContributorAppsAdminHasQQ(t *testing.T) {
	f := newContentFake()
	f.allFn = func(string, []any) ([]store.Row, error) { return contributorAggRows(), nil }
	w := callContent(newContentRouter(f), "contributor_apps", "", adminHdr())
	want := `{"code":0,"msg":"ok","data":[` +
		`{"id":10,"qq":"12345","name":"甲","bio":"b1","app_names":["应用A"]},` +
		`{"id":13,"qq":"67890","name":"乙","bio":"b2","app_names":["应用C"]}]}`
	if w.Body.String() != want {
		t.Fatalf("contributor_apps 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	if strings.Contains(w.Body.String(), `"avatar"`) || strings.Contains(w.Body.String(), `"app_id"`) {
		t.Fatalf("管理版无 avatar，且 app_id 白查不用: %s", w.Body.String())
	}
}

// ---------- stats：计数口径与类型 ----------

func TestContentStatsCountsAndTypes(t *testing.T) {
	f := newContentFake()
	f.valFn = func(q string, args []any) (any, bool, error) {
		switch {
		case strings.Contains(q, "SUM(download_count)"):
			return "340", true, nil // 字符串也要 (int) 成 340
		case strings.Contains(q, "CURDATE()"):
			return int64(4), true, nil
		case strings.Contains(q, "COUNT(*) FROM apps"):
			return int64(12), true, nil
		case strings.Contains(q, "COUNT(*) FROM clicks"):
			return int64(99), true, nil
		}
		return nil, false, nil
	}
	f.allFn = func(q string, args []any) ([]store.Row, error) {
		switch {
		case strings.Contains(q, "JOIN apps a") && strings.Contains(q, "CURDATE()"):
			return []store.Row{}, nil
		case strings.Contains(q, "JOIN apps a"):
			return []store.Row{srow("id", "7", "name", "N", "icon", "I", "cnt", "8")}, nil
		case strings.Contains(q, "JOIN pan_links l"):
			return []store.Row{srow("id", "2", "label", "A", "cnt", "5")}, nil
		}
		return nil, nil
	}
	w := callContent(newContentRouter(f), "stats", "", adminHdr())
	want := `{"code":0,"msg":"ok","data":{"total_apps":12,"total_downloads":340,"total_clicks":99,"today_clicks":4,"top_links":[{"id":"2","label":"A","cnt":"5"}],"top_apps":[{"id":7,"name":"N","icon":"I","cnt":8}],"top_apps_today":[]}}`
	if w.Body.String() != want {
		t.Fatalf("stats 逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// today_clicks 必须用 DB 的 CURDATE()。
	if len(f.find("QueryValue", "CURDATE()")) != 1 {
		t.Fatalf("today_clicks 未用 CURDATE()")
	}
}

// ---------- link_click：写库、截断、下载量 +1 ----------

func TestContentLinkClickWritesAndIncrements(t *testing.T) {
	f := newContentFake()
	f.rowFn = func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FROM pan_links WHERE id = ? AND is_active = 1") {
			return srow("id", int64(1), "app_id", int64(9), "url", "https://pan.x/y",
				"password", "p1", "is_active", 1), nil
		}
		return nil, nil
	}
	longUA := strings.Repeat("a", 300)
	hdr := map[string]string{
		"User-Agent":      longUA,
		"Referer":         "https://ref/x",
		"X-Forwarded-For": "1.2.3.4, 5.6.7.8",
	}
	w := callContentURL(newContentRouter(f), "/api.php?action=link_click&id=1", "", hdr)
	want := `{"code":0,"msg":"ok","data":{"url":"https:\/\/pan.x\/y","password":"p1"}}`
	if w.Body.String() != want {
		t.Fatalf("link_click 响应不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	ins := f.find("Exec", "INSERT INTO clicks")
	if len(ins) != 1 {
		t.Fatalf("clicks 必须写一条: %+v", f.calls)
	}
	// link_id, ip(XFF 整串), UA 截断 255 字节, referer
	if phpInt(ins[0].Args[0]) != 1 {
		t.Fatalf("link_id=%v", ins[0].Args[0])
	}
	if phpStr(ins[0].Args[1]) != "1.2.3.4, 5.6.7.8" {
		t.Fatalf("ip 必须是整串 XFF: %q", phpStr(ins[0].Args[1]))
	}
	ua := phpStr(ins[0].Args[2])
	if len(ua) != 255 {
		t.Fatalf("UA 必须字节截断到 255: len=%d", len(ua))
	}
	if phpStr(ins[0].Args[3]) != "https://ref/x" {
		t.Fatalf("referer=%q", phpStr(ins[0].Args[3]))
	}
	upd := f.find("Exec", "download_count = download_count + 1")
	if len(upd) != 1 || phpInt(upd[0].Args[0]) != 9 {
		t.Fatalf("下载量 +1 未按 app_id: %+v", upd)
	}
	assertCORS(t, w.Header())
}

func TestContentLinkClickInactiveOrMissing(t *testing.T) {
	f := newContentFake()
	f.rowFn = func(string, []any) (store.Row, error) { return nil, nil }
	w := callContentURL(newContentRouter(f), "/api.php?action=link_click&id=5", "", nil)
	assertError(t, w, http.StatusBadRequest, 1, "链接不存在")
}

// ---------- 管理接口：三态鉴权 ----------

func TestContentAdminAuthThreeStates(t *testing.T) {
	cases := []struct {
		action string
		body   string
		// 管理员成功态（true 表示 admin 调用应 HTTP 200 + code 0）
		adminOK bool
	}{
		{"app_update", `{"id":0,"name":"x"}`, true},
		{"app_delete", `{"id":0}`, true},
		{"banner_create", `{}`, true},
		{"category_delete", `{"id":0}`, true},
		{"contributor_update", `{"id":0,"name":"x"}`, true},
		{"link_update", `{"id":0,"label":"x"}`, true},
	}
	for _, tc := range cases {
		// 未登录 → code 401 / HTTP 401
		wNo := callContent(newContentRouter(newContentFake()), tc.action, tc.body, nil)
		assertError(t, wNo, http.StatusUnauthorized, 401, "未登录")
		// 普通用户 → code 403 / HTTP 400（json_out 映射）
		wUser := callContent(newContentRouter(newContentFake()), tc.action, tc.body, userHdr())
		assertError(t, wUser, http.StatusBadRequest, 403, "没有权限, 仅管理员可操作")
		// 管理员 → 成功
		if tc.adminOK {
			wAdmin := callContent(newContentRouter(newContentFake()), tc.action, tc.body, adminHdr())
			if wAdmin.Code != http.StatusOK || !strings.Contains(wAdmin.Body.String(), `"code":0`) {
				t.Fatalf("%s 管理员态异常: %d %s", tc.action, wAdmin.Code, wAdmin.Body.String())
			}
		}
	}
}

// 全部 15 个管理 action 的未登录态都必须是 401。
func TestContentAllAdminActionsRequireLogin(t *testing.T) {
	actions := []string{
		"app_create", "app_update", "app_delete",
		"banner_create", "banner_update", "banner_delete",
		"category_create", "category_update", "category_delete",
		"contributor_create", "contributor_update", "contributor_delete",
		"link_create", "link_update", "link_delete",
	}
	for _, a := range actions {
		w := callContent(newContentRouter(newContentFake()), a, `{}`, nil)
		assertError(t, w, http.StatusUnauthorized, 401, "未登录")
	}
}

// ---------- *_update：0 / "" / null 三态 ----------

func TestContentLinkUpdateNullVsEmptyVsZero(t *testing.T) {
	f := newContentFake()
	w := callContent(newContentRouter(f), "link_update",
		`{"id":1,"label":"","password":null,"url":"u"}`, adminHdr())
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("响应异常: %s", w.Body.String())
	}
	upd := f.find("Exec", "UPDATE pan_links SET")
	if len(upd) != 1 {
		t.Fatalf("应有一条 UPDATE: %+v", f.calls)
	}
	// 白名单顺序 pan_type,label,url,password,sort_order ⇒ 只组装 label="", url="u"，id 尾参。
	if len(upd[0].Args) != 3 || phpStr(upd[0].Args[0]) != "" || phpStr(upd[0].Args[1]) != "u" || phpInt(upd[0].Args[2]) != 1 {
		t.Fatalf("args 不对: %v (SQL=%s)", upd[0].Args, upd[0].Query)
	}
	if !strings.Contains(upd[0].Query, "label = ?, url = ?") {
		t.Fatalf("SET 子句不对: %s", upd[0].Query)
	}
	if strings.Contains(upd[0].Query, "is_active") {
		t.Fatalf("is_active 不在白名单: %s", upd[0].Query)
	}
}

// JSON 显式 null = 未传，必须继续下沉到 $_GET（PHP isset 中语义）。
func TestContentUpdateJSONNullFallsThroughToQuery(t *testing.T) {
	f := newContentFake()
	w := callContentURL(newContentRouter(f), "/api.php?action=link_update&label=fromget",
		`{"id":1,"label":null}`, adminHdr())
	if w.Code != http.StatusOK {
		t.Fatalf("响应异常: %d %s", w.Code, w.Body.String())
	}
	upd := f.find("Exec", "UPDATE pan_links SET")
	if len(upd) != 1 || len(upd[0].Args) != 2 || phpStr(upd[0].Args[0]) != "fromget" {
		t.Fatalf("JSON null 未下沉到 GET: %v", upd)
	}
}

func TestContentAppUpdateSpecialColumns(t *testing.T) {
	f := newContentFake()
	callContent(newContentRouter(f), "app_update",
		`{"id":1,"is_top":0,"is_featured":null,"pack_id":0,"release_date":""}`, adminHdr())
	upd := f.find("Exec", "UPDATE apps SET")
	if len(upd) != 1 {
		t.Fatalf("应有一条 UPDATE: %+v", f.calls)
	}
	// is_top=0、pack_id=NULL、release_date=NULL；is_featured 未传 → 不出现。
	if !strings.Contains(upd[0].Query, "pack_id = ?, is_top = ?, release_date = ?") {
		t.Fatalf("SET 子句不对: %s", upd[0].Query)
	}
	if len(upd[0].Args) != 4 {
		t.Fatalf("args 数不对: %v", upd[0].Args)
	}
	if upd[0].Args[0] != nil || phpInt(upd[0].Args[1]) != 0 || upd[0].Args[2] != nil || phpInt(upd[0].Args[3]) != 1 {
		t.Fatalf("args 语义不对: %v", upd[0].Args)
	}
	if strings.Contains(upd[0].Query, "is_featured") {
		t.Fatalf("显式 null 的 is_featured 不该出现: %s", upd[0].Query)
	}
}

func TestContentAppUpdateNewFlag(t *testing.T) {
	f := newContentFake()
	callContent(newContentRouter(f), "app_update", `{"id":1,"new_flag":1}`, adminHdr())
	upd := f.find("Exec", "UPDATE apps SET")
	if len(upd) != 1 || !strings.Contains(upd[0].Query, "new_until = DATE_ADD(NOW(), INTERVAL 3 DAY)") {
		t.Fatalf("new_flag=1 未标新 3 天: %+v", upd)
	}
	f2 := newContentFake()
	callContent(newContentRouter(f2), "app_update", `{"id":1,"new_flag":0}`, adminHdr())
	upd2 := f2.find("Exec", "UPDATE apps SET")
	if len(upd2) != 1 || !strings.Contains(upd2[0].Query, "new_until = NULL") {
		t.Fatalf("new_flag=0 未取消标新: %+v", upd2)
	}
	// 无字段且无 new_flag → 没有要更新的字段
	w := callContent(newContentRouter(newContentFake()), "app_update", `{"id":1}`, adminHdr())
	assertError(t, w, http.StatusBadRequest, 1, "没有要更新的字段")
}

func TestContentBannerUpdateAppIDZeroToNull(t *testing.T) {
	f := newContentFake()
	callContent(newContentRouter(f), "banner_update",
		`{"id":1,"is_active":0,"app_id":0,"title":null}`, adminHdr())
	upd := f.find("Exec", "UPDATE banners SET")
	if len(upd) != 1 {
		t.Fatalf("应有一条 UPDATE: %+v", f.calls)
	}
	// 白名单顺序 image,title,app_id,url,sort_order,is_active ⇒ is_active=0 先？不：app_id 在 is_active 前。
	if !strings.Contains(upd[0].Query, "app_id = ?, is_active = ?") {
		t.Fatalf("SET 子句不对: %s", upd[0].Query)
	}
	if len(upd[0].Args) != 3 || upd[0].Args[0] != nil || phpInt(upd[0].Args[1]) != 0 || phpInt(upd[0].Args[2]) != 1 {
		t.Fatalf("app_id=0 必须写 NULL、is_active=0 必须保留: %v", upd[0].Args)
	}
}

func TestContentCategoryUpdateRules(t *testing.T) {
	// 父分类不能是自己
	w := callContent(newContentRouter(newContentFake()), "category_update", `{"id":1,"parent_id":1}`, adminHdr())
	assertError(t, w, http.StatusBadRequest, 1, "父分类不能是自己")
	// 没有要更新的字段
	w2 := callContent(newContentRouter(newContentFake()), "category_update", `{"id":1}`, adminHdr())
	assertError(t, w2, http.StatusBadRequest, 1, "没有要更新的字段")
	// sort_order=0 必须写入（0 是合法值，不是"未传"）
	f := newContentFake()
	callContent(newContentRouter(f), "category_update", `{"id":1,"sort_order":0}`, adminHdr())
	upd := f.find("Exec", "UPDATE categories SET")
	if len(upd) != 1 || !strings.Contains(upd[0].Query, "sort_order = ?") || phpInt(upd[0].Args[0]) != 0 {
		t.Fatalf("sort_order=0 必须更新: %+v", upd)
	}
}

func TestContentContributorUpdateTrimsAll(t *testing.T) {
	f := newContentFake()
	callContent(newContentRouter(f), "contributor_update",
		`{"id":1,"name":" 甲 ","bio":"","sort_order":" 5 "}`, adminHdr())
	upd := f.find("Exec", "UPDATE contributors SET")
	if len(upd) != 1 || len(upd[0].Args) != 4 {
		t.Fatalf("应有一条 UPDATE 带 3 值+id: %+v", upd)
	}
	if phpStr(upd[0].Args[0]) != "甲" || phpStr(upd[0].Args[1]) != "" || phpStr(upd[0].Args[2]) != "5" {
		t.Fatalf("三个字段都要 trim: %v", upd[0].Args)
	}
}

func TestContentCreateValidations(t *testing.T) {
	// category_create：name='0' 也是空
	w := callContent(newContentRouter(newContentFake()), "category_create", `{"name":"0"}`, adminHdr())
	assertError(t, w, http.StatusBadRequest, 1, "分类名不能为空")
	// 父分类不存在
	f := newContentFake()
	f.valFn = func(q string, args []any) (any, bool, error) { return nil, false, nil }
	w2 := callContent(newContentRouter(f), "category_create", `{"name":"工具","parent_id":9}`, adminHdr())
	assertError(t, w2, http.StatusBadRequest, 1, "父分类不存在")
	// link_create：url 为空
	w3 := callContent(newContentRouter(newContentFake()), "link_create", `{"app_id":1,"url":""}`, adminHdr())
	assertError(t, w3, http.StatusBadRequest, 1, "链接地址不能为空")
	// link_create 成功：默认 label/pan_type
	f3 := newContentFake()
	w4 := callContent(newContentRouter(f3), "link_create", `{"app_id":3,"url":"u"}`, adminHdr())
	if w4.Body.String() != `{"code":0,"msg":"ok","data":{"id":55}}` {
		t.Fatalf("link_create 响应: %s", w4.Body.String())
	}
	ins := f3.find("Exec", "INSERT INTO pan_links")
	if len(ins) != 1 || phpStr(ins[0].Args[1]) != "uc" || phpStr(ins[0].Args[2]) != "UC网盘" {
		t.Fatalf("默认 pan_type/label 不对: %v", ins)
	}
	// contributor_create：格式错 / 重复
	w5 := callContent(newContentRouter(newContentFake()), "contributor_create", `{"qq":"123"}`, adminHdr())
	assertError(t, w5, http.StatusBadRequest, 1, "QQ 号格式不正确")
	f5 := newContentFake()
	f5.valFn = func(string, []any) (any, bool, error) { return int64(1), true, nil }
	w6 := callContent(newContentRouter(f5), "contributor_create", `{"qq":"12345"}`, adminHdr())
	assertError(t, w6, http.StatusBadRequest, 1, "该 QQ 已在投稿名单中")
	// app_create：软件名不能为空
	w7 := callContent(newContentRouter(newContentFake()), "app_create", `{}`, adminHdr())
	assertError(t, w7, http.StatusBadRequest, 1, "软件名不能为空")
}

func TestContentCategoryDeleteOrder(t *testing.T) {
	f := newContentFake()
	w := callContent(newContentRouter(f), "category_delete", `{"id":4}`, adminHdr())
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("响应异常: %s", w.Body.String())
	}
	if len(f.calls) != 3 {
		t.Fatalf("应为 鉴权 + 两条写: %+v", f.calls)
	}
	if !strings.Contains(f.calls[1].Query, "UPDATE categories SET parent_id = 0") {
		t.Fatalf("第一步必须是子分类升级: %s", f.calls[1].Query)
	}
	if !strings.Contains(f.calls[2].Query, "DELETE FROM categories") {
		t.Fatalf("第二步才是删除: %s", f.calls[2].Query)
	}
}
