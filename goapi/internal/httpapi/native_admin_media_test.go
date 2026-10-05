package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5B-2 的 12 个后台 action 单测：全部走脚本化假 DB / 假对象存储，
// 不连真实 MySQL / Redis，不调用任何真实接口。

const mediaS3Public = "https://media-test.example.com"

func newMediaRouter(db accountsDB, video *store.VideoConfig, docroot string) *Router {
	cfg := config.Load()
	cfg.PHPDocRoot = docroot
	cfg.S3PublicURL = mediaS3Public
	cfg.S3Endpoint = "https://endpoint-test.example.com"
	env := Env{Cfg: cfg, Log: testLogger(), Accounts: db}
	if video != nil {
		env.VideoCfg = fixedVideoCfg{cfg: *video}
	}
	return New(env)
}

func mediaQueries(f *lotteryFake, match string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method != "Exec" && strings.Contains(c.Query, match) {
			out = append(out, c)
		}
	}
	return out
}

func mediaAdminCall(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json",
		map[string]string{"Authorization": "Bearer " + lotteryAdminToken})
}

func mediaCallNoToken(rt *Router, action string) *httptest.ResponseRecorder {
	return callAction(rt, "GET", "/api.php?action="+action, "", "", nil)
}

// ---------- ① 鉴权四态 ----------

func TestAdminMediaAuthFourStates(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")

	// 无 token → 401 未登录
	w := mediaCallNoToken(rt, "admin_video_config_get")
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"未登录","data":null}` {
		t.Fatalf("无 token 应 401 未登录: code=%d body=%s", w.Code, w.Body.String())
	}
	assertCORS(t, w.Header())

	// 坏 token → 401 登录已失效
	w = callAction(rt, "GET", "/api.php?action=admin_video_config_get", "", "", map[string]string{"Authorization": "Bearer bad"})
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"登录已失效","data":null}` {
		t.Fatalf("坏 token 应 401 登录已失效: code=%d body=%s", w.Code, w.Body.String())
	}

	// 普通用户 → JSON code=403，HTTP 状态码 400
	w = callAction(rt, "GET", "/api.php?action=admin_video_config_get", "", "", map[string]string{"Authorization": "Bearer " + lotteryUserToken})
	if w.Code != 400 || w.Body.String() != `{"code":403,"msg":"没有权限, 仅管理员可操作","data":null}` {
		t.Fatalf("普通用户应 HTTP400+code403: code=%d body=%s", w.Code, w.Body.String())
	}

	// 管理员 → 进入业务逻辑
	w = callAction(rt, "GET", "/api.php?action=admin_video_config_get", "", "", map[string]string{"Authorization": "Bearer " + lotteryAdminToken})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("管理员应成功: code=%d body=%s", w.Code, w.Body.String())
	}
}

// 12 个 action 无 token 一律 401 未登录（一律先鉴权）。
func TestAdminMediaAllActionsUnauthorized(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	for _, a := range []string{
		"about_config_set", "notice_set", "admin_video_config_get", "admin_video_config_set",
		"admin_video_clean", "crash_reports", "harm_reports", "harm_report_status",
		"harm_report_delete", "uc_status", "uc_logout", "version_update",
	} {
		w := mediaCallNoToken(rt, a)
		if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"未登录","data":null}` {
			t.Fatalf("%s 无 token 应 401 未登录: code=%d body=%s", a, w.Code, w.Body.String())
		}
	}
}

// ---------- ② admin_video_config get/set 键序与类型 ----------

func TestAdminVideoConfigGetGolden(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "")
	t.Setenv("PHP_POST_MAX_SIZE", "")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "admin_video_config_get", "")
	want := `{"code":0,"msg":"ok","data":{"enabled":1,"max_mb":100,"total_limit_mb":600,"keep_days":0,"auto_clean":1,` +
		`"usage":{"count":0,"total_bytes":0,"max_size":0,"max_size_id":0,"oldest_at":"","oldest_id":0,"cleaned_count":0},` +
		`"server_upload_max_mb":0}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	assertCORS(t, w.Header())
}

func TestAdminVideoConfigGetServerMaxFromEnv(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "150")
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "")
	t.Setenv("PHP_POST_MAX_SIZE", "")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "admin_video_config_get", "")
	if !strings.Contains(w.Body.String(), `"server_upload_max_mb":150}`) {
		t.Fatalf("server_upload_max_mb 应为 150: %s", w.Body.String())
	}
}

func TestAdminVideoServerUploadMaxMBIniFallback(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "150M")
	t.Setenv("PHP_POST_MAX_SIZE", "1G")
	if got := videoServerMaxMB(); got != 150 {
		t.Fatalf("ini 取小应为 150, got=%d", got)
	}
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "-1")
	t.Setenv("PHP_POST_MAX_SIZE", "0")
	if got := videoServerMaxMB(); got != 0 {
		t.Fatalf("不限应为 0, got=%d", got)
	}
}

func TestAdminVideoConfigSetValidation(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "")
	t.Setenv("PHP_POST_MAX_SIZE", "")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")

	cases := []struct {
		body string
		msg  string
	}{
		{`{"max_mb":0}`, "单个视频上限不能小于 1MB"},
		{`{"max_mb":501}`, "单个视频不能超过 500MB"},
		{`{"max_mb":100,"total_limit_mb":99}`, "总容量上限不能小于 100MB"},
		{`{"max_mb":100,"total_limit_mb":100001}`, "总容量上限不能超过 100000MB"},
		{`{"max_mb":100,"total_limit_mb":600,"keep_days":-1}`, "保留天数不能为负数"},
		{`{"max_mb":100,"total_limit_mb":600,"keep_days":3651}`, "保留天数不能超过 3650 天"},
	}
	for _, tc := range cases {
		w := mediaAdminCall(t, rt, "admin_video_config_set", tc.body)
		if w.Code != 400 {
			t.Fatalf("body=%s 应 HTTP400, got=%d", tc.body, w.Code)
		}
		want := `{"code":1,"msg":"` + tc.msg + `","data":null}`
		if w.Body.String() != want {
			t.Fatalf("body=%s\n got=%s\nwant=%s", tc.body, w.Body.String(), want)
		}
	}

	// total < max 必须在该顺序上被拦。
	w := mediaAdminCall(t, rt, "admin_video_config_set", `{"max_mb":200,"total_limit_mb":100}`)
	if w.Body.String() != `{"code":1,"msg":"总容量上限不能小于单个视频上限","data":null}` {
		t.Fatalf("total<max: %s", w.Body.String())
	}
}

func TestAdminVideoConfigSetServerMaxBlocks(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "100")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "admin_video_config_set", `{"max_mb":150,"total_limit_mb":600}`)
	want := `{"code":1,"msg":"单个视频上限不能超过服务器上传上限 (100MB), 请先调大 php.ini 的 upload_max_filesize","data":null}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

func TestAdminVideoConfigSetSave(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "")
	t.Setenv("PHP_POST_MAX_SIZE", "")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "admin_video_config_set",
		`{"max_mb":120,"total_limit_mb":700,"keep_days":7,"enabled":0,"auto_clean":0}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	want := `{"code":0,"msg":"ok","data":{"enabled":0,"max_mb":120,"total_limit_mb":700,"keep_days":7,"auto_clean":0,` +
		`"usage":{"count":0,"total_bytes":0,"max_size":0,"max_size_id":0,"oldest_at":"","oldest_id":0,"cleaned_count":0}}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	// 落库的 JSON 键序与类型
	execs := f.execs("INSERT INTO `settings`")
	if len(execs) != 1 {
		t.Fatalf("应写一次 settings, got=%d", len(execs))
	}
	gotSQL := execs[0].Query
	if !strings.Contains(gotSQL, "VALUES ('video_config', ?)") {
		t.Fatalf("SQL 不一致: %s", gotSQL)
	}
	if got := phpStr(execs[0].Args[0]); got != `{"enabled":0,"max_mb":120,"total_limit_mb":700,"keep_days":7,"auto_clean":0}` {
		t.Fatalf("落库 JSON 不一致: %s", got)
	}
}

// 未提供的键必须回落「旧配置」（对齐 param($k, 旧值)）。
func TestAdminVideoConfigSetDefaultsFromOld(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	t.Setenv("PHP_UPLOAD_MAX_FILESIZE", "")
	t.Setenv("PHP_POST_MAX_SIZE", "")
	f := newLotteryFake()
	old := store.VideoConfig{Enabled: 0, MaxMB: 120, TotalLimitMB: 600, KeepDays: 30, AutoClean: 0}
	rt := newMediaRouter(f, &old, "")
	w := mediaAdminCall(t, rt, "admin_video_config_set", `{"total_limit_mb":800}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	if got := phpStr(f.execs("INSERT INTO `settings`")[0].Args[0]); got != `{"enabled":0,"max_mb":120,"total_limit_mb":800,"keep_days":30,"auto_clean":0}` {
		t.Fatalf("旧值回落不一致: %s", got)
	}
}

// ---------- ③ admin_video_clean ----------

func videoRowsFn(main []store.Row, pm []store.Row) func(string, []any) ([]store.Row, error) {
	return func(q string, _ []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM `social_messages`") {
			return main, nil
		}
		if strings.Contains(q, "FROM `social_pm_messages`") {
			return pm, nil
		}
		return nil, nil
	}
}

func TestVideoCleanModeInvalid(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "admin_video_clean", `{"mode":"bogus"}`)
	if w.Body.String() != `{"code":1,"msg":"清理模式不合法","data":null}` {
		t.Fatalf("模式不合法文案不一致: %s", w.Body.String())
	}
}

func TestVideoCleanAll(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	f := newLotteryFake()
	f.allFn = videoRowsFn(
		[]store.Row{srow("id", 1, "video", mediaS3Public+"/a.mp4", "video_size", 1048576, "created_at", "2026-01-01 00:00:00")},
		[]store.Row{srow("id", 2, "video", "https://other/x.mp4", "video_size", 524288, "created_at", "2026-01-02 00:00:00")},
	)
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "admin_video_clean", `{"mode":"all"}`)
	want := `{"code":0,"msg":"ok","data":{"deleted":2,"freed_bytes":1572864,"visited":2,"skipped":"","mode":"all","freed_mb":1.5,` +
		`"usage":{"count":0,"total_bytes":0,"max_size":0,"max_size_id":0,"oldest_at":"","oldest_id":0,"cleaned_count":0}}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	ups := f.execs("UPDATE `social_messages` SET video = ''")
	if len(ups) != 1 || phpInt(ups[0].Args[0]) != 1 {
		t.Fatalf("social_messages 应清 1 条: %+v", ups)
	}
	ups = f.execs("UPDATE `social_pm_messages` SET video = ''")
	if len(ups) != 1 || phpInt(ups[0].Args[0]) != 2 {
		t.Fatalf("social_pm_messages 应清 1 条: %+v", ups)
	}
}

func TestVideoCleanOverLimitByCapacity(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	f := newLotteryFake()
	f.allFn = videoRowsFn(
		[]store.Row{srow("id", 1, "video", mediaS3Public+"/a.mp4", "video_size", 1048576, "created_at", "2026-01-01 00:00:00")},
		[]store.Row{srow("id", 2, "video", "https://other/x.mp4", "video_size", 524288, "created_at", "2026-01-02 00:00:00")},
	)
	cfg := store.VideoConfig{Enabled: 1, MaxMB: 100, TotalLimitMB: 1, KeepDays: 0, AutoClean: 1}
	rt := newMediaRouter(f, &cfg, "")
	w := mediaAdminCall(t, rt, "admin_video_clean", `{"mode":"over_limit"}`)
	want := `{"code":0,"msg":"ok","data":{"deleted":1,"freed_bytes":1048576,"visited":2,"skipped":"","mode":"over_limit","freed_mb":1.0,` +
		`"usage":{"count":0,"total_bytes":0,"max_size":0,"max_size_id":0,"oldest_at":"","oldest_id":0,"cleaned_count":0}}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	if len(f.execs("UPDATE `social_messages` SET video = ''")) != 1 {
		t.Fatalf("应只清最旧的 social_messages")
	}
	if len(f.execs("UPDATE `social_pm_messages` SET video = ''")) != 0 {
		t.Fatalf("容量已够，不应清 social_pm_messages")
	}
}

// 手动清理不受 auto_clean=0 限制；keep_days 也不参与（byDays=false）。
func TestVideoCleanIgnoresAutoCleanAndKeepDays(t *testing.T) {
	t.Setenv("VIDEO_SERVER_MAX_MB", "")
	f := newLotteryFake()
	f.allFn = videoRowsFn(
		[]store.Row{srow("id", 1, "video", mediaS3Public+"/a.mp4", "video_size", 2097152, "created_at", "2000-01-01 00:00:00")},
		nil,
	)
	// auto_clean=0：over_limit 仍按容量删；keep_days=1：因 byDays=false 不按天数删。
	cfg := store.VideoConfig{Enabled: 1, MaxMB: 100, TotalLimitMB: 1, KeepDays: 1, AutoClean: 0}
	rt := newMediaRouter(f, &cfg, "")
	w := mediaAdminCall(t, rt, "admin_video_clean", `{"mode":"over_limit"}`)
	if !strings.Contains(w.Body.String(), `"deleted":1`) || !strings.Contains(w.Body.String(), `"skipped":""`) {
		t.Fatalf("auto_clean=0 不应阻止手动清理: %s", w.Body.String())
	}

	// 换成容量充足：不删（天数不生效）
	f2 := newLotteryFake()
	f2.allFn = videoRowsFn(
		[]store.Row{srow("id", 1, "video", mediaS3Public+"/a.mp4", "video_size", 1024, "created_at", "2000-01-01 00:00:00")},
		nil,
	)
	cfg2 := store.VideoConfig{Enabled: 1, MaxMB: 100, TotalLimitMB: 100000, KeepDays: 1, AutoClean: 1}
	rt2 := newMediaRouter(f2, &cfg2, "")
	w2 := mediaAdminCall(t, rt2, "admin_video_clean", `{"mode":"over_limit"}`)
	if !strings.Contains(w2.Body.String(), `"deleted":0`) || !strings.Contains(w2.Body.String(), `"visited":1`) {
		t.Fatalf("byDays=false 不应按天数删: %s", w2.Body.String())
	}
}

// ---------- 假对象存储（直测 cleanupVideoRow 的删除路径） ----------

type fakeMediaObjects struct {
	deleted []string
}

func (f *fakeMediaObjects) KeyFromURL(u string) string {
	if strings.HasPrefix(u, mediaS3Public+"/") {
		return strings.TrimPrefix(u, mediaS3Public+"/")
	}
	return ""
}
func (f *fakeMediaObjects) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func TestVideoCleanupRowDeletesObject(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	req := httptest.NewRequest("GET", "/api.php?action=admin_video_clean", nil)
	c := rt.env.newCtx(httptest.NewRecorder(), req, "admin_video_clean")
	obj := &fakeMediaObjects{}
	var res videoCleanResult
	row := store.VideoRow{Table: "social_messages", ID: 7, Video: mediaS3Public + "/chat/abc.mp4", Size: 2048, CreatedAt: "2026-01-01 00:00:00"}
	rt.cleanupVideoRow(c, obj, row, &res)
	if len(obj.deleted) != 1 || obj.deleted[0] != "chat/abc.mp4" {
		t.Fatalf("应先删对象: %+v", obj.deleted)
	}
	if res.deleted != 1 || res.freed != 2048 {
		t.Fatalf("成功改库后应计入: %+v", res)
	}
	if len(f.execs("UPDATE `social_messages` SET video = ''")) != 1 {
		t.Fatalf("应写 UPDATE")
	}

	// 非本站 URL：KeyFromURL 为空 → 不删对象，但仍置空。
	obj2 := &fakeMediaObjects{}
	var res2 videoCleanResult
	rt.cleanupVideoRow(c, obj2, store.VideoRow{Table: "social_pm_messages", ID: 8, Video: "https://other/x.mp4", Size: 1}, &res2)
	if len(obj2.deleted) != 0 {
		t.Fatalf("非本站 URL 不应删对象: %+v", obj2.deleted)
	}
	if res2.deleted != 1 {
		t.Fatalf("仍应清消息: %+v", res2)
	}
}

// ---------- ④ crash_reports / harm_reports ----------

func TestCrashReportsList(t *testing.T) {
	f := newLotteryFake()
	f.allFn = func(q string, _ []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM crash_reports") {
			return []store.Row{srow(
				"id", 3, "device", "Pixel", "android_version", "14", "app_version", "1.2.3",
				"stack", "boom", "ip", "1.2.3.4", "created_at", "2026-06-01 12:00:00")}, nil
		}
		return nil, nil
	}
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "crash_reports", "")
	want := `{"code":0,"msg":"ok","data":[{"id":"3","device":"Pixel","android_version":"14","app_version":"1.2.3",` +
		`"stack":"boom","ip":"1.2.3.4","created_at":"2026-06-01 12:00:00"}]}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	qs := mediaQueries(f, "FROM crash_reports")
	if len(qs) != 1 || !strings.Contains(qs[0].Query, "ORDER BY id DESC LIMIT 100") {
		t.Fatalf("SQL 不一致: %+v", qs)
	}
}

func TestCrashReportsEmpty(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "crash_reports", "")
	if w.Body.String() != `{"code":0,"msg":"ok","data":[]}` {
		t.Fatalf("空集合必须是 []: %s", w.Body.String())
	}
}

func TestHarmReportsList(t *testing.T) {
	f := newLotteryFake()
	f.allFn = func(q string, _ []any) ([]store.Row, error) {
		if strings.Contains(q, "FROM harm_reports") {
			return []store.Row{srow(
				"id", 5, "app_id", 9, "app_name", "某App", "content", "被和谐了", "contact", "qq",
				"status", 0, "ip", "1.2.3.4", "created_at", "2026-06-02 10:00:00")}, nil
		}
		return nil, nil
	}
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "harm_reports", "")
	want := `{"code":0,"msg":"ok","data":[{"id":"5","app_id":"9","app_name":"某App","content":"被和谐了",` +
		`"contact":"qq","status":"0","ip":"1.2.3.4","created_at":"2026-06-02 10:00:00"}]}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	qs := mediaQueries(f, "FROM harm_reports")
	if len(qs) != 1 || !strings.Contains(qs[0].Query, "ORDER BY status ASC, id DESC LIMIT 200") {
		t.Fatalf("SQL 不一致: %+v", qs)
	}
}

// ---------- ⑤ harm_report_status / delete ----------

func TestHarmReportStatusSQL(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "harm_report_status", `{"id":5,"status":1}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	e := f.execs("UPDATE harm_reports SET status = ?")
	if len(e) != 1 || phpInt(e[0].Args[0]) != 1 || phpInt(e[0].Args[1]) != 5 {
		t.Fatalf("参数错误: %+v", e)
	}

	// 非 1 一律置 0（状态机）
	w = mediaAdminCall(t, rt, "harm_report_status", `{"id":5,"status":2}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	e = f.execs("UPDATE harm_reports SET status = ?")
	if len(e) != 2 || phpInt(e[1].Args[0]) != 0 || phpInt(e[1].Args[1]) != 5 {
		t.Fatalf("status=2 应写 0: %+v", e)
	}

	// 缺 id → 0（PHP 不做存在性校验）
	w = mediaAdminCall(t, rt, "harm_report_status", `{"status":1}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	e = f.execs("UPDATE harm_reports SET status = ?")
	if len(e) != 3 || phpInt(e[2].Args[1]) != 0 {
		t.Fatalf("缺 id 应为 0: %+v", e)
	}
}

func TestHarmReportDeleteSQL(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "harm_report_delete", `{"id":9}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	e := f.execs("DELETE FROM harm_reports WHERE id = ?")
	if len(e) != 1 || phpInt(e[0].Args[0]) != 9 {
		t.Fatalf("删除参数错误: %+v", e)
	}
	// 不存在的 id（缺省 0）也照样执行、照样 ok
	w = mediaAdminCall(t, rt, "harm_report_delete", `{}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	e = f.execs("DELETE FROM harm_reports WHERE id = ?")
	if len(e) != 2 || phpInt(e[1].Args[0]) != 0 {
		t.Fatalf("缺 id 应为 0: %+v", e)
	}
}

// ---------- ⑥ uc_status / uc_logout 文件语义 ----------

func writeUCAuth(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "uc_auth.php"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUcStatusFile(t *testing.T) {
	dir := t.TempDir()
	writeUCAuth(t, dir, "<?php\nreturn array (\n  'cookie' => 'abc\\'def',\n  'ts' => 1700000000,\n  'nickname' => '昵称',\n  'account' => 'u@x',\n);\n")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, dir)
	w := mediaAdminCall(t, rt, "uc_status", "")
	want := `{"code":0,"msg":"ok","data":{"logged_in":true,"login_time":1700000000,"nickname":"昵称","account":"u@x","cookie_len":7}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	assertCORS(t, w.Header())
}

func TestUcStatusNotLoggedIn(t *testing.T) {
	dir := t.TempDir()
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, dir)
	// 文件不存在
	w := mediaAdminCall(t, rt, "uc_status", "")
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"logged_in":false}}` {
		t.Fatalf("无文件应未登录: %s", w.Body.String())
	}
	// cookie="0"：PHP empty("0") 为真 → 未登录
	writeUCAuth(t, dir, "<?php\nreturn array (\n  'cookie' => '0',\n  'nickname' => 'x',\n);\n")
	w = mediaAdminCall(t, rt, "uc_status", "")
	if w.Body.String() != `{"code":0,"msg":"ok","data":{"logged_in":false}}` {
		t.Fatalf("cookie=0 应未登录: %s", w.Body.String())
	}
}

func TestUcLogoutRemovesFile(t *testing.T) {
	dir := t.TempDir()
	writeUCAuth(t, dir, "<?php\nreturn array (\n  'cookie' => 'x',\n);\n")
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, dir)
	w := mediaAdminCall(t, rt, "uc_logout", "")
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "uc_auth.php")); !os.IsNotExist(err) {
		t.Fatalf("uc_auth.php 应被删除: err=%v", err)
	}
}

// ---------- ⑦ version_update ----------

func lastSettingJSON(t *testing.T, f *lotteryFake) string {
	t.Helper()
	e := f.execs("INSERT INTO `settings`")
	if len(e) == 0 {
		t.Fatal("没有写 settings")
	}
	return phpStr(e[len(e)-1].Args[0])
}

func TestVersionUpdateValidation(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	// 版本号为空：注意 release_date 默认先算、size_mb 先算，最后才报这个错。
	w := mediaAdminCall(t, rt, "version_update", `{"version":"","release_date":"2026-01-02"}`)
	if w.Body.String() != `{"code":1,"msg":"版本号不能为空","data":null}` {
		t.Fatalf("文案不一致: %s", w.Body.String())
	}
	if w.Code != 400 {
		t.Fatalf("应 HTTP400: %d", w.Code)
	}
	// "0" 也是 falsy
	w = mediaAdminCall(t, rt, "version_update", `{"version":"0","release_date":"2026-01-02"}`)
	if w.Body.String() != `{"code":1,"msg":"版本号不能为空","data":null}` {
		t.Fatalf("version=0 应报错: %s", w.Body.String())
	}
	if len(f.execs("INSERT INTO `settings`")) != 0 {
		t.Fatalf("报错时不应写库")
	}
}

func TestVersionUpdateSave(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "version_update",
		`{"version":"2.0.0","url":"https://example.com/app.apk","update_log":"更新说明","update_mode":"external","force_update":1,"size_mb":12.5,"release_date":"2026-01-02"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	got := lastSettingJSON(t, f)
	want := `{"version":"2.0.0","url":"https:\/\/example.com\/app.apk","update_log":"更新说明","update_mode":"external","force_update":1,"size_mb":12.5,"release_date":"2026-01-02"}`
	if got != want {
		t.Fatalf("落库 JSON 不一致\n got=%s\nwant=%s", got, want)
	}
}

func TestVersionUpdateReleaseDateDefaultsToday(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "version_update", `{"version":"1.0.1"}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	today := time.Now().In(config.LocalZone()).Format("2006-01-02")
	if !strings.Contains(lastSettingJSON(t, f), `"release_date":"`+today+`"`) {
		t.Fatalf("release_date 应默认今天 %s: %s", today, lastSettingJSON(t, f))
	}
	// size_mb 未提供 → (float)0 → 0.0
	if !strings.Contains(lastSettingJSON(t, f), `"size_mb":0.0`) {
		t.Fatalf("size_mb 应为浮点 0.0: %s", lastSettingJSON(t, f))
	}
}

func TestVersionUpdateLocalFileSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "uploads", "apk"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 1.5MB
	if err := os.WriteFile(filepath.Join(dir, "uploads", "apk", "app.apk"), make([]byte, 1572864), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, dir)
	w := mediaAdminCall(t, rt, "version_update",
		`{"version":"1.1","url":"https://h/uploads/apk/app.apk","release_date":"2026-01-02"}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(lastSettingJSON(t, f), `"size_mb":1.5`) {
		t.Fatalf("应从本地文件算 1.5MB: %s", lastSettingJSON(t, f))
	}
}

func TestVersionUpdateHeadSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("应为 HEAD, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "2097152") // 2MB
		w.WriteHeader(200)
	}))
	defer srv.Close()

	f := newLotteryFake()
	cfgDir := t.TempDir()
	rt := newMediaRouter(f, nil, cfgDir)
	rt.env.Cfg.S3PublicURL = srv.URL
	w := mediaAdminCall(t, rt, "version_update",
		`{"version":"1.2","url":"`+srv.URL+`/a.apk","release_date":"2026-01-02"}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(lastSettingJSON(t, f), `"size_mb":2.0`) {
		t.Fatalf("应从 HEAD 算 2.0MB: %s", lastSettingJSON(t, f))
	}
}

// 已显式给 size_mb 时不覆盖，也不做 HEAD。
func TestVersionUpdateExplicitSizeNotOverridden(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "version_update",
		`{"version":"1.3","url":"https://example.com/a.apk","size_mb":9,"release_date":"2026-01-02"}`)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(lastSettingJSON(t, f), `"size_mb":9.0`) {
		t.Fatalf("显式 size_mb 应为 9.0: %s", lastSettingJSON(t, f))
	}
}

// ---------- 设置写入 ----------

func TestAdminAboutConfigSetSave(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "about_config_set", `{"website":"https://a/b","banner_text":"标题"}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	got := phpStr(f.execs("VALUES ('about_config', ?)")[0].Args[0])
	want := `{"qq_group":"","qq_key":"","qq_url":"","qq_channel":"","website":"https:\/\/a\/b","github":"","feedback":"","donate":"","banner_text":"标题","banner_sub":""}`
	if got != want {
		t.Fatalf("落库 JSON 不一致\n got=%s\nwant=%s", got, want)
	}
}

func TestAdminNoticeSetSave(t *testing.T) {
	f := newLotteryFake()
	rt := newMediaRouter(f, nil, "")
	w := mediaAdminCall(t, rt, "notice_set", `{"content":"维护","mode":"every","enabled":3}`)
	if w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("应成功: %s", w.Body.String())
	}
	got := phpStr(f.execs("VALUES ('notice', ?)")[0].Args[0])
	if got != `{"content":"维护","mode":"every","enabled":1}` {
		t.Fatalf("enabled 应归一化为 1: %s", got)
	}
}
