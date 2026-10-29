package uc

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// ---------- qrCreate ----------

func TestQRCreateSuccess(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getTokenForQrcodeLogin", 200,
		`{"status":2000000,"data":{"members":{"token":"TK123"}}}`)

	res := QRCreate()
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "token", res.Data["token"], "TK123")
	mustValue(t, "expires_in", res.Data["expires_in"], int64(600))
	qr := phpStr(res.Data["qr_url"])
	for _, want := range []string{
		"https://su.uc.cn/1_n0ZCv?uc_param_str=dsdnfrpfbivesscpgimibtbmnijblauputogpintnwktprchmt",
		"&token=TK123&client_id=381",
		"&uc_biz_str=S%3Acustom%7CC%3Atitlebar_fix",
	} {
		if !strings.Contains(qr, want) {
			t.Fatalf("qr_url 缺少 %q:\n%s", want, qr)
		}
	}

	reqs := f.reqsFor("/cas/getTokenForQrcodeLogin")
	if len(reqs) != 1 {
		t.Fatalf("应只请求一次 CAS，实际 %d", len(reqs))
	}
	r := reqs[0]
	mustEqual(t, "method", r.Method, "POST")
	if !regexp.MustCompile(`^__dt=792565&__t=\d{13}$`).MatchString(r.RawQuery) {
		t.Fatalf("CAS query 不符: %q", r.RawQuery)
	}
	if !regexp.MustCompile(`^client_id=381&v=1\.2&request_id=\d{13}$`).MatchString(r.Body) {
		t.Fatalf("CAS body 顺序/内容不符: %q", r.Body)
	}
	// header 逐字（Go 会把名字规范化，值必须原样）。
	mustEqual(t, "ua", r.Header.Get("User-Agent"), UA)
	mustEqual(t, "origin", r.Header.Get("Origin"), "https://drive.uc.cn")
	mustEqual(t, "referer", r.Header.Get("Referer"), "https://drive.uc.cn/")
	mustEqual(t, "content-type", r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
}

func TestQRCreateStatusNotOK(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getTokenForQrcodeLogin", 200, `{"status":4000400,"message":"风控拦截"}`)
	res := QRCreate()
	mustEqual(t, "code", res.Code, 502)
	mustEqual(t, "msg", res.Msg, "获取二维码失败：风控拦截")
	if res.Data != nil {
		t.Fatalf("失败不应有 data: %#v", res.Data)
	}
}

func TestQRCreateNonJSON(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getTokenForQrcodeLogin", 500, "<html>oops</html>")
	res := QRCreate()
	mustEqual(t, "code", res.Code, 502)
	mustEqual(t, "msg", res.Msg, "UC 登录接口无响应 (HTTP 500)")
}

func TestQRCreateEmptyToken(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getTokenForQrcodeLogin", 200, `{"status":2000000,"data":{"members":{"token":""}}}`)
	res := QRCreate()
	mustEqual(t, "code", res.Code, 502)
	mustEqual(t, "msg", res.Msg, "二维码 token 为空")
}

// ---------- qrPoll ----------

func TestQRPollEmptyToken(t *testing.T) {
	f := withFakeUC(t)
	res := QRPoll("")
	mustEqual(t, "code", res.Code, 400)
	mustEqual(t, "msg", res.Msg, "缺少 token")
	if n := len(f.all()); n != 0 {
		t.Fatalf("空 token 不应发请求，实际 %d 次", n)
	}
}

func TestQRPollNonJSON(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getServiceTicketByQrcodeToken", 502, "bad gateway")
	res := QRPoll("TK")
	mustEqual(t, "code", res.Code, 502)
	mustEqual(t, "msg", res.Msg, "轮询接口无响应 (HTTP 502)")
}

func TestQRPollWaiting(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getServiceTicketByQrcodeToken", 200, `{"status":4000100,"message":"等待扫码"}`)
	res := QRPoll("TK")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "state", res.Data["state"], "waiting")
	mustValue(t, "uc_status", res.Data["uc_status"], int64(4000100))
	mustValue(t, "msg", res.Data["msg"], "等待扫码")
}

func TestQRPollScanned(t *testing.T) {
	f := withFakeUC(t)
	// 4000101 直接判定已扫描
	f.reply("/cas/getServiceTicketByQrcodeToken", 200, `{"status":4000101}`)
	res := QRPoll("TK")
	mustValue(t, "state(4000101)", res.Data["state"], "scanned")

	// 文案含 scan（不区分大小写）也判定已扫描
	f.reply("/cas/getServiceTicketByQrcodeToken", 200, `{"status":1,"message":"QRCode Scanned, waiting confirm"}`)
	res = QRPoll("TK")
	mustValue(t, "state(msg=Scanned)", res.Data["state"], "scanned")

	// 请求体带 token，顺序 client_id/v/request_id/token
	reqs := f.reqsFor("/cas/getServiceTicketByQrcodeToken")
	body := reqs[len(reqs)-1].Body
	if !regexp.MustCompile(`^client_id=381&v=1\.2&request_id=\d{13}&token=TK$`).MatchString(body) {
		t.Fatalf("轮询 body 不符: %q", body)
	}
}

func TestQRPollNoTicket(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getServiceTicketByQrcodeToken", 200, `{"status":2000000,"data":{"members":{}}}`)
	res := QRPoll("TK")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "state", res.Data["state"], "waiting")
	mustValue(t, "msg", res.Data["msg"], "尚未确认")
	if _, ok := res.Data["uc_status"]; ok {
		t.Fatalf("未确认分支不该带 uc_status: %#v", res.Data)
	}
}

func TestQRPollSuccess(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getServiceTicketByQrcodeToken", 200,
		`{"status":2000000,"data":{"members":{"ticket":"TICKET-1"}}}`)
	f.reply("/drive/account/info", 200, "ok", "ctoken=ct1; Path=/; Secure")
	f.reply("/drive/", 200, "ok", "__puus=p1; Path=/")
	f.reply("/drive/main", 200, "ok")
	f.reply("/drive/account/setting", 200, "ok")
	f.reply("/drive/list", 200, "ok")
	f.reply("/api/user/info", 200, `{"code":0,"data":{"nickname":"小明","mask_mobile":"138****8888"}}`)

	res := QRPoll("TK")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "state", res.Data["state"], "ok")
	mustValue(t, "nickname", res.Data["nickname"], "小明")

	// 五步页面都走了，且首步带 st=<ticket>（rawurlencode）。
	for _, p := range []string{"/drive/account/info", "/drive/", "/drive/main", "/drive/account/setting", "/drive/list"} {
		if n := len(f.reqsFor(p)); n != 1 {
			t.Fatalf("%s 应请求 1 次，实际 %d", p, n)
		}
	}
	first := f.reqsFor("/drive/account/info")[0]
	mustEqual(t, "st 参数", first.RawQuery, "st=TICKET-1")
	mustEqual(t, "页面 accept", first.Header.Get("Accept"),
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	// /api/user/info 带累积 cookie（插入序 ctoken → __puus）
	ui := f.reqsFor("/api/user/info")
	if len(ui) != 2 {
		t.Fatalf("/api/user/info 应请求 2 次（ticketToCookie + fetchUserInfo），实际 %d", len(ui))
	}
	mustEqual(t, "首轮 cookie", ui[0].Header.Get("Cookie"), "ctoken=ct1; __puus=p1")
	mustEqual(t, "x-ctoken", ui[0].Header.Get("X-Ctoken"), "ct1")

	// 凭据已落盘：cookie 含两个值，nickname/account 来自 user/info，
	// qrPoll 保存的四个键（无 source）。
	a := LoadAuth()
	if !strings.Contains(a.Cookie, "ctoken=ct1") || !strings.Contains(a.Cookie, "__puus=p1") {
		t.Fatalf("保存的 cookie 不符: %q", a.Cookie)
	}
	mustEqual(t, "saved nickname", a.Nickname, "小明")
	mustEqual(t, "saved account", a.Account, "138****8888")
	mustEqual(t, "saved source", a.Source, "")
	if a.TS <= 0 {
		t.Fatalf("ts 应为当前时间戳，得到 %d", a.TS)
	}
}

func TestQRPollFollowsRelativeRedirect(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getServiceTicketByQrcodeToken", 200,
		`{"status":2000000,"data":{"members":{"service_ticket":"ST-9"}}}`)
	// 用 302 + 相对 Location("/step2")：跳转目标必须拼上 DriveBase 变成 /drive/step2。
	f.on("/drive/account/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/step2")
		w.WriteHeader(http.StatusFound)
	})
	f.reply("/drive/step2", 200, "ok", "ctoken=from-redirect; Path=/")
	f.reply("/drive/", 200, "ok")
	f.reply("/drive/main", 200, "ok")
	f.reply("/drive/account/setting", 200, "ok")
	f.reply("/drive/list", 200, "ok")
	f.reply("/api/user/info", 200, `{"code":0,"data":{"nickname":"n"}}`)

	res := QRPoll("TK")
	mustEqual(t, "code", res.Code, 0)
	steps := f.reqsFor("/drive/step2")
	if len(steps) != 1 {
		t.Fatalf("应跟随相对 Location 一次，实际 %d", len(steps))
	}
	// service_ticket 兜底路径
	if !strings.Contains(LoadAuth().Cookie, "ctoken=from-redirect") {
		t.Fatalf("跳转后的 Set-Cookie 应被吸收: %q", LoadAuth().Cookie)
	}
}

func TestQRPollCookieExchangeFails(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/cas/getServiceTicketByQrcodeToken", 200,
		`{"status":2000000,"data":{"members":{"ticket":"TK"}}}`)
	for _, p := range []string{"/drive/account/info", "/drive/", "/drive/main", "/drive/account/setting", "/drive/list"} {
		f.reply(p, 200, "ok")
	}
	f.reply("/api/user/info", 200, `{"code":0,"data":{}}`)
	res := QRPoll("TK")
	mustEqual(t, "code", res.Code, 502)
	mustEqual(t, "msg", res.Msg, "换取 Cookie 失败，请重试")
	if got := LoadAuth(); got != (Auth{}) {
		t.Fatalf("失败不该写凭据: %#v", got)
	}
}

// ---------- loginByCookie / fetchUserInfo ----------

func TestLoginByCookieEmpty(t *testing.T) {
	f := withFakeUC(t)
	res := LoginByCookie("   ")
	mustEqual(t, "code", res.Code, 400)
	mustEqual(t, "msg", res.Msg, "Cookie 为空")
	if n := len(f.all()); n != 0 {
		t.Fatalf("空 Cookie 不应发请求，实际 %d", n)
	}
}

func TestLoginByCookieSuccess(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/user/info", 200, `{"code":0,"data":{"nickname":"老王","mask_mobile":"139****0000"}}`)
	res := LoginByCookie(" ctoken=abc; __puus=1 ")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "verified", res.Data["verified"], true)
	mustValue(t, "nickname", res.Data["nickname"], "老王")

	a := LoadAuth()
	// PHP 先 trim 再保存
	mustEqual(t, "saved cookie", a.Cookie, "ctoken=abc; __puus=1")
	mustEqual(t, "saved nickname", a.Nickname, "老王")
	mustEqual(t, "saved account", a.Account, "139****0000")
	mustEqual(t, "saved source", a.Source, "manual")
	if a.TS <= 0 {
		t.Fatalf("ts 应为当前时间戳: %d", a.TS)
	}
	// 请求带上原始 cookie 与 x-ctoken
	r := f.reqsFor("/api/user/info")[0]
	mustEqual(t, "cookie 头", r.Header.Get("Cookie"), "ctoken=abc; __puus=1")
	mustEqual(t, "x-ctoken", r.Header.Get("X-Ctoken"), "abc")
	mustEqual(t, "method", r.Method, "GET")
}

func TestLoginByCookieUnverifiedStillSaves(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/user/info", 200, `{"code":-1,"message":"未登录"}`)
	res := LoginByCookie("ctoken=bad")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "verified", res.Data["verified"], false)
	mustValue(t, "nickname", res.Data["nickname"], "")
	// PHP 是无条件保存
	mustEqual(t, "saved cookie", LoadAuth().Cookie, "ctoken=bad")
	mustEqual(t, "saved source", LoadAuth().Source, "manual")
}

func TestFetchUserInfoBranches(t *testing.T) {
	f := withFakeUC(t)
	// 非 JSON → ok=false
	f.reply("/api/user/info", 200, "not json")
	res := FetchUserInfo("ctoken=1")
	mustEqual(t, "非 JSON code", res.Code, 401)
	mustValue(t, "非 JSON ok", res.Data["ok"], false)

	// code != 0 → ok=false 并带 raw
	f.reply("/api/user/info", 200, `{"code":1234,"message":"token expired"}`)
	res = FetchUserInfo("ctoken=1")
	mustEqual(t, "code!=0", res.Code, 401)
	mustValue(t, "raw 保留", res.Data["raw"].(map[string]any)["code"], float64(1234))

	// nickname 缺失时回退 name
	f.reply("/api/user/info", 200, `{"code":0,"data":{"name":"回退名"}}`)
	res = FetchUserInfo("ctoken=1")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "nickname 回退", res.Data["nickname"], "回退名")
	mustValue(t, "account 缺省", res.Data["account"], "")

	// data 为空对象
	f.reply("/api/user/info", 200, `{"code":0}`)
	res = FetchUserInfo("ctoken=1")
	mustEqual(t, "无 data", res.Code, 0)
	mustValue(t, "无 data nickname", res.Data["nickname"], "")
}
