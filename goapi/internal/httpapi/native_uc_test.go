package httpapi

// 阶段 6：UC 网盘 5 个 action 的接线单测。
//
// 只覆盖不依赖 S3 的分支（本机没有对象存储）：
//   - 鉴权前置（无 token → 401 未登录）
//   - 上游 UC 的 code → HTTP 状态映射（502 / 400 / 401）
//   - 成功响应的键序（PHP 的 data 数组顺序）
//   - uc_import 的 kind= 过滤（kind=none 时两个分支都跳过，不碰 S3）
// 真正的「下载 → 压缩 → 传 S3」链路必须在服务器上与 PHP 9101 双跑验证。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/uc"
)

// withFakeUC 把 uc 包的三个 base 指向本地假服务，并在测试结束时还原。
// hand 返回 false 表示这个路径没被处理（回 404，便于发现漏配的假响应）。
func withFakeUC(t *testing.T, hand func(w http.ResponseWriter, r *http.Request) bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hand == nil || !hand(w, r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":-1,"message":"unexpected path ` + r.URL.Path + `"}`))
		}
	}))
	oldAPI, oldCAS, oldDrive := uc.APIBase, uc.CASBase, uc.DriveBase
	uc.APIBase, uc.CASBase, uc.DriveBase = srv.URL+"/api", srv.URL+"/cas", srv.URL+"/drive"
	t.Cleanup(func() {
		uc.APIBase, uc.CASBase, uc.DriveBase = oldAPI, oldCAS, oldDrive
		srv.Close()
	})
}

// writeUCAuthCookie 在 docroot 里写一份 PHP var_export 风格的 uc_auth.php。
// （native_admin_media_test.go 里已有同名不同签名的 writeUCAuth，这里换个名字避免重定义。）
func writeUCAuthCookie(t *testing.T, docroot, cookie string) {
	t.Helper()
	body := "<?php\n// UC 网盘登录态（自动生成）\nreturn array (\n"
	body += "  'cookie' => '" + cookie + "',\n"
	body += "  'ts' => 1700000000,\n"
	body += "  'nickname' => '测试昵称',\n"
	body += "  'account' => '138****8888',\n"
	body += ");\n"
	if err := os.WriteFile(filepath.Join(docroot, "uc_auth.php"), []byte(body), 0o600); err != nil {
		t.Fatalf("写 uc_auth.php 失败: %v", err)
	}
}

func jsonBody(s string) func(w http.ResponseWriter, _ *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s))
	}
}

// ---------- uc_qr_create ----------

func TestUCQRCreateGolden(t *testing.T) {
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/cas/getTokenForQrcodeLogin" {
			return false
		}
		jsonBody(`{"status":2000000,"data":{"members":{"token":"TK-9"}}}`)(w, r)
		return true
	})
	rt := newMediaRouter(newLotteryFake(), nil, "")
	w := mediaAdminCall(t, rt, "uc_qr_create", "")
	if w.Code != 200 {
		t.Fatalf("HTTP = %d, 期望 200; body=%s", w.Code, w.Body.String())
	}
	// 键序 token/qr_url/expires_in，qr_url 里的 uc_biz_str 必须是 rawurlencode 过的。
	want := `{"code":0,"msg":"ok","data":{"token":"TK-9","qr_url":"https:\/\/su.uc.cn\/1_n0ZCv?uc_param_str=dsdnfrpfbivesscpgimibtbmnijblauputogpintnwktprchmt&token=TK-9&client_id=381&uc_biz_str=S%3Acustom%7CC%3Atitlebar_fix","expires_in":600}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("响应体不一致\n got: %s\nwant: %s", got, want)
	}
	assertCORS(t, w.Header())
}

func TestUCQRCreateUpstreamFail(t *testing.T) {
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/cas/getTokenForQrcodeLogin" {
			return false
		}
		jsonBody(`{"status":4000000,"message":"系统繁忙"}`)(w, r)
		return true
	})
	rt := newMediaRouter(newLotteryFake(), nil, "")
	w := mediaAdminCall(t, rt, "uc_qr_create", "")
	// PHP: json_error($uc['msg'], 502)
	if w.Code != 400 || w.Body.String() != `{"code":502,"msg":"获取二维码失败：系统繁忙","data":null}` {
		t.Fatalf("应 502 + 获取二维码失败: code=%d body=%s", w.Code, w.Body.String())
	}
}

// ---------- uc_qr_poll ----------

func TestUCQRPollMissingToken(t *testing.T) {
	// token 为空时 uc.QRPoll 直接返回 400，不发任何请求。
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool { return false })
	rt := newMediaRouter(newLotteryFake(), nil, "")
	w := mediaAdminCall(t, rt, "uc_qr_poll", `{}`)
	if w.Code != 400 || w.Body.String() != `{"code":400,"msg":"缺少 token","data":null}` {
		t.Fatalf("应 HTTP400/code400 缺少 token: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUCQRPollScanned(t *testing.T) {
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/cas/getServiceTicketByQrcodeToken" {
			return false
		}
		jsonBody(`{"status":4000101,"message":"Scanned"}`)(w, r)
		return true
	})
	rt := newMediaRouter(newLotteryFake(), nil, "")
	w := mediaAdminCall(t, rt, "uc_qr_poll", `{"token":"TK-9"}`)
	want := `{"code":0,"msg":"ok","data":{"state":"scanned","uc_status":4000101,"msg":"Scanned"}}`
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("已扫码分支不一致: code=%d body=%s", w.Code, w.Body.String())
	}
}

// ---------- uc_cookie_set ----------

func TestUCCookieSetEmpty(t *testing.T) {
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool { return false })
	rt := newMediaRouter(newLotteryFake(), nil, "")
	w := mediaAdminCall(t, rt, "uc_cookie_set", `{"cookie":"   "}`)
	// PHP: json_error($uc['msg'], 400)
	if w.Code != 400 || w.Body.String() != `{"code":400,"msg":"Cookie 为空","data":null}` {
		t.Fatalf("应 400 Cookie 为空: code=%d body=%s", w.Code, w.Body.String())
	}
}

// ---------- uc_resolve ----------

func TestUCResolveNotLoggedIn(t *testing.T) {
	dir := t.TempDir() // 没有 uc_auth.php
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool { return false })
	rt := newMediaRouter(newLotteryFake(), nil, dir)
	w := mediaAdminCall(t, rt, "uc_resolve", `{"url":"https://drive.uc.cn/s/abc"}`)
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"UC 账号未登录，请先在后台扫码登录","data":null}` {
		t.Fatalf("应 401 未登录: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUCResolveBadLink(t *testing.T) {
	dir := t.TempDir()
	writeUCAuthCookie(t, dir, "ck=1")
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool { return false })
	rt := newMediaRouter(newLotteryFake(), nil, dir)
	w := mediaAdminCall(t, rt, "uc_resolve", `{"url":"https://example.com/not-uc"}`)
	if w.Code != 400 || w.Body.String() != `{"code":400,"msg":"无法识别 UC 分享链接","data":null}` {
		t.Fatalf("应 400 无法识别: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUCResolveNeedPwd(t *testing.T) {
	dir := t.TempDir()
	writeUCAuthCookie(t, dir, "ck=1")
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/share/sharepage/token" {
			return false
		}
		jsonBody(`{"code":40004,"message":"passcode required"}`)(w, r)
		return true
	})
	rt := newMediaRouter(newLotteryFake(), nil, dir)
	w := mediaAdminCall(t, rt, "uc_resolve", `{"url":"https://drive.uc.cn/s/abc"}`)
	// need_pwd 必须映射成 400（不是 403）
	if w.Code != 400 || w.Body.String() != `{"code":400,"msg":"提取码不正确或需要提取码","data":null}` {
		t.Fatalf("应 400 需要提取码: code=%d body=%s", w.Code, w.Body.String())
	}
}

// ---------- uc_import ----------

func TestUCImportNotLoggedIn(t *testing.T) {
	dir := t.TempDir()
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool { return false })
	rt := newMediaRouter(newLotteryFake(), nil, dir)
	w := mediaAdminCall(t, rt, "uc_import", `{"url":"https://drive.uc.cn/s/abc"}`)
	// 注意 uc_import 的文案与 uc_resolve 不同（没有「在后台」）
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"UC 账号未登录，请先扫码登录","data":null}` {
		t.Fatalf("应 401: code=%d body=%s", w.Code, w.Body.String())
	}
}

// uc_import 的鉴权在登录态检查之前：没 token 时连 auth 文件都不读。
func TestUCImportAuthFirst(t *testing.T) {
	rt := newMediaRouter(newLotteryFake(), nil, t.TempDir())
	w := mediaCallNoToken(rt, "uc_import")
	if w.Code != 401 || w.Body.String() != `{"code":401,"msg":"未登录","data":null}` {
		t.Fatalf("应 401 未登录: code=%d body=%s", w.Code, w.Body.String())
	}
}

// kind=none 时两个导入分支都跳过 → 空结果，且完全不碰 S3（env.S3 为 nil 也不会 panic）。
func TestUCImportKindNone(t *testing.T) {
	dir := t.TempDir()
	writeUCAuthCookie(t, dir, "ck=1")
	withFakeUC(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/share/sharepage/token":
			jsonBody(`{"code":0,"data":{"stoken":"ST-1"}}`)(w, r)
			return true
		case "/api/share/sharepage/detail":
			jsonBody(`{"code":0,"data":{"list":[]}}`)(w, r)
			return true
		}
		return false
	})
	rt := newMediaRouter(newLotteryFake(), nil, dir)
	w := mediaAdminCall(t, rt, "uc_import", `{"url":"https://drive.uc.cn/s/abc","kind":"none"}`)
	want := `{"code":0,"msg":"ok","data":{"images":[],"apk":null,"errors":[]}}`
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("kind=none 应返回空结果: code=%d body=%s", w.Code, w.Body.String())
	}
}
