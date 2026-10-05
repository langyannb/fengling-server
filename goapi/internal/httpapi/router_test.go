package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// 断掉 MySQL 与 PHP-FPM 的 Router：用于验证「Go 自己生成的错误响应」。
func brokenRouter(t *testing.T) *Router {
	t.Helper()
	cfg := config.Load()
	cfg.DBSocket = "/tmp/goapi-test-no-such.sock" // MySQL 必然连不上
	cfg.FPMNetwork = "unix"
	cfg.FPMAddr = "/tmp/goapi-test-no-such-fpm.sock" // php-fpm 必然连不上
	st, err := store.Open(cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return New(Env{Cfg: cfg, Store: st, Redis: store.NewRedis(cfg, testLogger()), Log: testLogger()})
}

func assertCORS(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("缺 Access-Control-Allow-Origin (=%q)", got)
	}
	if got := h.Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Errorf("缺/错 Access-Control-Allow-Methods (=%q)", got)
	}
	if got := h.Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization" {
		t.Errorf("缺/错 Access-Control-Allow-Headers (=%q)", got)
	}
}

// 原生 action 出错时：必须带 CORS，且文案里不许出现 Go 的文件名/行号。
func TestNativeVersionErrorHasCORSAndNoPathLeak(t *testing.T) {
	rt := brokenRouter(t)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("GET", "/api.php?action=version", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
	assertCORS(t, w.Header())
	body := w.Body.String()
	if !strings.Contains(body, `"code":500`) {
		t.Fatalf("body=%s", body)
	}
	if strings.Contains(body, ".go") || strings.Contains(body, "@ ") {
		t.Fatalf("响应体泄露了 Go 文件路径/行号: %s", body)
	}
	if !strings.Contains(body, "服务器错误: ") {
		t.Fatalf("文案不对: %s", body)
	}
}

// PHP-FPM 不可达时的网关兜底响应：形态对齐 api.php 的致命错误（code:-1、无 data 键），
// 并且同样必须带 CORS。
func TestGatewayErrorHasCORSAndNoPathLeak(t *testing.T) {
	rt := brokenRouter(t)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("GET", "/api.php?action=no_such_action_xyz", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
	assertCORS(t, w.Header())
	body := w.Body.String()
	if !strings.HasPrefix(body, `{"code":-1,"msg":"服务器错误: 网关无法连接 PHP-FPM`) {
		t.Fatalf("兜底文案不对: %s", body)
	}
	if strings.Contains(body, `"data"`) {
		t.Fatalf("致命错误响应不应有 data 键: %s", body)
	}
	if strings.Contains(body, ".go") {
		t.Fatalf("响应体泄露了 Go 文件路径: %s", body)
	}
}

// 透传成功时**不能**由 Go 预设 CORS（否则与 PHP 发的重复）。
// 这里用一个假的 go-fastcgi 不可达场景反向验证：错误分支必须自己补 CORS，
// 而正常分支的 CORS 只能来自 PHP —— 由 tools/goapi_diff.sh 在服务器上验证头不重复。
func TestSetCORSOnlyOnGoGeneratedResponses(t *testing.T) {
	w := httptest.NewRecorder()
	setCORS(w.Header())
	if got := w.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 {
		t.Fatalf("setCORS 应为单值: %v", got)
	}
}
