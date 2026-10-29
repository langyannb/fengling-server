package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/config"
)

// newFallbackRouter 造一个只关心 default 分支的 Router：
// PHP_FALLBACK 由参数决定，不注入任何 DB（fallback 关闭时压根不该碰数据库/FPM）。
func newFallbackRouter(t *testing.T, fallback bool) *Router {
	t.Helper()
	cfg := config.Load()
	cfg.PHPFallback = fallback
	// 故意把 fpm 指到一个必然连不上的地址：测试若真的走了透传会立刻报错，
	// 从而证明「关掉 fallback 后完全不连 PHP-FPM」这件事是被测出来的，不是碰巧。
	cfg.FPMNetwork = "unix"
	cfg.FPMAddr = "/nonexistent/php-cgi-test.sock"
	return New(Env{Cfg: cfg, Log: testLogger()})
}

// TestUnknownActionWithFallbackOff 阶段6：PHP 退休后，未注册 action 必须
// 逐字对齐 api.php 的 default 分支 —— json_out(null, 404, '未知操作: ' . $action)。
func TestUnknownActionWithFallbackOff(t *testing.T) {
	rt := newFallbackRouter(t, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api.php?action=no_such_action", nil)
	rt.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("HTTP 状态 = %d, 期望 404", rec.Code)
	}
	want := `{"code":404,"msg":"未知操作: no_such_action","data":null}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body 不逐字一致\n得到: %s\n期望: %s", got, want)
	}
	// CORS 必须照发（PHP 在 switch 之前无条件 header）
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

// TestUnknownActionWithFallbackOnHitsFPM 反向证明：fallback 开着时仍然走透传，
// 所以这里会拿到 502（连不上 fpm），证明开关真的在控制两条不同路径。
func TestUnknownActionWithFallbackOnHitsFPM(t *testing.T) {
	rt := newFallbackRouter(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api.php?action=no_such_action", nil)
	rt.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "未知操作") {
		t.Fatalf("fallback 开着时不该由 Go 自己回未知操作，得到: %s", body)
	}
	if rec.Code == 404 {
		t.Errorf("fallback 开着时应由 PHP 决定状态码，Go 直接 404 说明走向错了: %s", body)
	}
}
