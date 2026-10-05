package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 阶段 1 的 6 个 action 必须在 Go 里**原生**处理（不再透传 PHP）。
//
// 这里只覆盖不需要 MySQL/S3 的分支：鉴权前置、OPTIONS 头、/healthz。
// 「真实上传」必须在服务器上与 PHP 9101 双跑（本机没有 PHP/MySQL/S3），
// 见 README 阶段 1 的自证说明。

// uploadActions 是本单原生的 5 个上传 action（video_config 单独测）。
var uploadActions = []struct {
	action    string
	noToken   string // 无 token 时的文案
	expectMsg int
}{
	{"user_avatar", "登录已失效", 401},
	{"social_image_upload", "登录已失效", 401},
	{"social_video_upload", "登录已失效", 401},
	{"upload", "未登录", 401},
	{"upload_apk", "未登录", 401},
}

func TestNativeUploadActionsRejectAnonymous(t *testing.T) {
	rt := brokenRouter(t)
	for _, c := range uploadActions {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, httptest.NewRequest("POST", "/api.php?action="+c.action, strings.NewReader("")))
		if w.Code != c.expectMsg {
			t.Fatalf("%s: status=%d body=%s", c.action, w.Code, w.Body.String())
		}
		assertCORS(t, w.Header())
		if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("%s: content-type=%q", c.action, ct)
		}
		body := w.Body.String()
		if !strings.Contains(body, `"msg":"`+c.noToken+`"`) {
			t.Fatalf("%s: 文案不对: %s", c.action, body)
		}
		if !strings.Contains(body, `"code":401`) || !strings.Contains(body, `"data":null`) {
			t.Fatalf("%s: 壳不对: %s", c.action, body)
		}
		// 原生处理过的证据：PCRE 文案里没出现 Go 文件路径，也没有走 PHP 透传。
		if strings.Contains(body, ".go") {
			t.Fatalf("%s: 泄露路径: %s", c.action, body)
		}
	}
}

// 阶段 0 的对齐差异：PHP 的 OPTIONS/204 仍带默认 Content-Type。
func TestOptionsAddsPHPDefaultContentType(t *testing.T) {
	rt := brokenRouter(t)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("OPTIONS", "/api.php?action=version", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
	assertCORS(t, w.Header())
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=UTF-8" {
		t.Fatalf("content-type=%q, 期望 PHP 默认值 text/html; charset=UTF-8", got)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 不应有响应体: %q", w.Body.String())
	}
}

// /healthz：展示最近一次视频清理时间（Redis 没启用时也要安全返回 0）。
func TestHealthzReportsVideoCleanup(t *testing.T) {
	rt := brokenRouter(t)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"video_last_cleanup":0`) {
		t.Fatalf("body=%s", body)
	}
	if !strings.HasPrefix(body, `{"code":0,"msg":"ok","data":`) {
		t.Fatalf("壳不对: %s", body)
	}
}

// 用**真实**的 net/http 传输层再验一次 OPTIONS：httptest.NewRecorder 只记录 Header 设置，
// 不能证明 net/http 真的把 Content-Type 写进了 204 响应（历史上 204 会丢 Content-Type）。
func TestOptionsContentTypeOverRealTransport(t *testing.T) {
	srv := httptest.NewServer(brokenRouter(t))
	defer srv.Close()

	req, err := http.NewRequest("OPTIONS", srv.URL+"/api.php?action=version", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=UTF-8" {
		t.Fatalf("真实响应头 Content-Type=%q, 期望 text/html; charset=UTF-8", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("CORS=%q", got)
	}
}
