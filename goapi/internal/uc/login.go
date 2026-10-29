package uc

import (
	"strconv"
	"strings"
	"time"
)

// 本文件对齐 uc.php 的「扫码登录」段（uc.php:162-297）。

// QRCreate 对齐 UcDrive::qrCreate()（uc.php:164-184）。
//
// 失败一律 code 502（api.php:3110 把它映射成 HTTP 502）。
func QRCreate() Result {
	t := ts()
	body := phpBuildQuery([][2]string{
		{"client_id", ClientID},
		{"v", "1.2"},
		{"request_id", t},
	})
	r := req(casURL("/getTokenForQrcodeLogin?__dt=792565&__t="+t), reqOpt{
		method:  "POST",
		headers: casHeaders(),
		body:    body,
	})
	j, isArr := decodeArray(r.body)
	if !isArr {
		return Result{Code: 502, Msg: "UC 登录接口无响应 (HTTP " + strconv.Itoa(r.status) + ")"}
	}
	st := int64(0)
	if isset(j, "status") {
		st = phpInt(j["status"])
	}
	if st != 2000000 {
		msg := "未知"
		if isset(j, "message") {
			msg = phpStr(j["message"])
		}
		return Result{Code: 502, Msg: "获取二维码失败：" + msg}
	}
	token := ""
	if isset(j, "data") {
		members := asMap(asMap(j["data"])["members"])
		if isset(members, "token") {
			token = phpStr(members["token"])
		}
	}
	if token == "" {
		return Result{Code: 502, Msg: "二维码 token 为空"}
	}
	qr := "https://su.uc.cn/1_n0ZCv?uc_param_str=dsdnfrpfbivesscpgimibtbmnijblauputogpintnwktprchmt" +
		"&token=" + token + "&client_id=" + ClientID +
		"&uc_biz_str=" + phpRawURLEncode("S:custom|C:titlebar_fix")
	return Result{Code: 0, Data: map[string]any{
		"token":      token,
		"qr_url":     qr,
		"expires_in": int64(600),
	}}
}

// QRPoll 对齐 UcDrive::qrPoll()（uc.php:186-220）。
//
// 注意 PHP 的返回谱系，api.php:3116 据此映射 HTTP：
//
//	code 400（缺少 token）            → HTTP 400
//	code 502（轮询接口/换 Cookie 失败）→ HTTP 502
//	code 0  + data.state              → HTTP 200
//
// 扫码成功（ticket 到手）时会顺带 saveAuth()，凭据落到 AuthPath。
func QRPoll(token string) Result {
	if token == "" {
		return Result{Code: 400, Msg: "缺少 token"}
	}
	t := ts()
	body := phpBuildQuery([][2]string{
		{"client_id", ClientID},
		{"v", "1.2"},
		{"request_id", t},
		{"token", token},
	})
	r := req(casURL("/getServiceTicketByQrcodeToken"), reqOpt{
		method:  "POST",
		headers: casHeaders(),
		body:    body,
	})
	j, isArr := decodeArray(r.body)
	if !isArr {
		return Result{Code: 502, Msg: "轮询接口无响应 (HTTP " + strconv.Itoa(r.status) + ")"}
	}
	st := int64(0)
	if isset(j, "status") {
		st = phpInt(j["status"])
	}
	if st != 2000000 {
		msg := ""
		if isset(j, "message") {
			msg = phpStr(j["message"])
		}
		state := "waiting"
		if strings.Contains(strings.ToLower(msg), "scan") || st == 4000101 {
			state = "scanned"
		}
		return Result{Code: 0, Data: map[string]any{
			"state":     state,
			"uc_status": st,
			"msg":       msg,
		}}
	}
	members := asMap(asMap(j["data"])["members"])
	ticket := ""
	if !phpEmpty(members["ticket"]) {
		ticket = phpStr(members["ticket"])
	} else if !phpEmpty(members["service_ticket"]) {
		ticket = phpStr(members["service_ticket"])
	}
	if ticket == "" {
		return Result{Code: 0, Data: map[string]any{
			"state": "waiting",
			"msg":   "尚未确认",
		}}
	}
	cookie := ticketToCookie(ticket)
	if cookie == "" {
		return Result{Code: 502, Msg: "换取 Cookie 失败，请重试"}
	}
	info := FetchUserInfo(cookie)
	SaveAuth(Auth{
		Cookie:   cookie,
		TS:       time.Now().Unix(),
		Nickname: phpStr(info.Data["nickname"]),
		Account:  phpStr(info.Data["account"]),
	})
	return Result{Code: 0, Data: map[string]any{
		"state":    "ok",
		"nickname": phpStr(info.Data["nickname"]),
	}}
}

// ticketToCookie 对齐 UcDrive::ticketToCookie()（uc.php:222-262）：
// 用 serviceTicket 走一遍 UC 各页面累积 Cookie，最后调一次 /user/info 再收 Set-Cookie。
//
// 页面顺序与每步最多 6 次重定向（相对 Location 前缀 DriveBase）逐字照抄。
func ticketToCookie(ticket string) string {
	jar := newCookieJar()
	steps := []string{
		DriveBase + "/account/info?st=" + phpRawURLEncode(ticket),
		DriveBase + "/",
		DriveBase + "/main",
		DriveBase + "/account/setting",
		DriveBase + "/list",
	}
	for _, u := range steps {
		cur := u
		n := 0
		for cur != "" && n < 6 {
			n++
			r := req(cur, reqOpt{
				method: "GET",
				cookie: jar.String(),
				headers: []string{
					"user-agent: " + UA,
					"accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
					"referer: https://drive.uc.cn/",
				},
			})
			jar.absorb(r.setCookies)
			if r.status >= 300 && r.status < 400 && r.location != "" {
				if strings.HasPrefix(r.location, "http") {
					cur = r.location
				} else {
					cur = DriveBase + r.location
				}
				continue
			}
			break
		}
	}
	ck := jar.String()
	r := req(apiURL("/user/info"), reqOpt{
		method:  "GET",
		cookie:  ck,
		headers: apiHeaders(ck),
	})
	jar.absorb(r.setCookies)
	return jar.String()
}

// LoginByCookie 对齐 UcDrive::loginByCookie()（uc.php:264-278）。
//
// 手工 Cookie 兜底：**无条件**保存（verify 结果只回传 verified），
// 出错只有「Cookie 为空」一种 → code 400（api.php:3121 映射成 HTTP 400）。
func LoginByCookie(cookie string) Result {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return Result{Code: 400, Msg: "Cookie 为空"}
	}
	info := FetchUserInfo(cookie)
	ok := false
	if v, exists := info.Data["ok"]; exists {
		ok, _ = v.(bool)
	}
	nick := phpStr(info.Data["nickname"])
	acct := phpStr(info.Data["account"])
	SaveAuth(Auth{
		Cookie:   cookie,
		TS:       time.Now().Unix(),
		Nickname: nick,
		Account:  acct,
		Source:   "manual",
	})
	return Result{Code: 0, Data: map[string]any{
		"verified": ok,
		"nickname": nick,
	}}
}

// FetchUserInfo 对齐 UcDrive::fetchUserInfo()（uc.php:280-297）：
// 调 /user/info 验证 Cookie。PHP 返回 ['ok'=>bool,'nickname','account','raw']，
// Go 侧映射成 Result：Code 0 = ok=true，Code 401 = ok=false（Cookie 无效）。
// Data 里保留 ok/nickname/account（失败时另有 raw）。
func FetchUserInfo(cookie string) Result {
	r := req(apiURL("/user/info"), reqOpt{
		method:  "GET",
		cookie:  cookie,
		headers: apiHeaders(cookie),
	})
	j, isArr := decodeArray(r.body)
	if !isArr {
		return Result{Code: 401, Msg: "Cookie 无效", Data: map[string]any{"ok": false}}
	}
	code := int64(-1)
	if isset(j, "code") {
		code = phpInt(j["code"])
	}
	if code != 0 {
		return Result{Code: 401, Msg: "Cookie 无效", Data: map[string]any{"ok": false, "raw": j}}
	}
	d := asMap(j["data"])
	nick := ""
	if isset(d, "nickname") {
		nick = phpStr(d["nickname"])
	} else if isset(d, "name") {
		nick = phpStr(d["name"])
	}
	acct := ""
	if isset(d, "mask_mobile") {
		acct = phpStr(d["mask_mobile"])
	}
	return Result{Code: 0, Data: map[string]any{
		"ok":       true,
		"nickname": nick,
		"account":  acct,
	}}
}
