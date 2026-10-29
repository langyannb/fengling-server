package uc

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loginForTest 写一份凭据，让 Resolve 通过「已登录」检查。
func loginForTest(t *testing.T) {
	t.Helper()
	if !SaveAuth(Auth{Cookie: "ctoken=ct; __puus=p", TS: 1759000000, Nickname: "n"}) {
		t.Fatalf("SaveAuth 失败")
	}
}

func detailJSON(pdir string) string {
	if pdir == "d1" {
		return `{"code":0,"data":{"list":[
			{"file_name":"inner.zip","size":2048,"fid":"f3","share_fid_token":"t4","updated_at":"1759000003","dir":false,"file":true}
		]}}`
	}
	return `{"code":0,"data":{"list":[
		{"file_name":"Setup.apk","size":1048576,"fid":"f1","share_fid_token":"t1","updated_at":"1759000000","dir":false,"file":true},
		{"file_name":"主图.PNG","size":10485760,"fid":"f2","share_fid_token":"t2","updated_at":"1759000001","dir":false,"file":true},
		{"file_name":"子目录","fid":"d1","share_fid_token":"t3","updated_at":"1759000002","dir":true,"file":false,"file_type":1}
	]}}`
}

func TestResolveNotLoggedIn(t *testing.T) {
	withFakeUC(t)
	res := Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "code", res.Code, 401)
	mustEqual(t, "msg", res.Msg, "UC 账号未登录，请先在后台扫码登录")
}

func TestResolveBadShareURL(t *testing.T) {
	f := withFakeUC(t)
	loginForTest(t)
	res := Resolve("这不是链接", "")
	mustEqual(t, "code", res.Code, 400)
	mustEqual(t, "msg", res.Msg, "无法识别 UC 分享链接")
	if n := len(f.all()); n != 0 {
		t.Fatalf("认不出链接时不该发请求，实际 %d", n)
	}
}

func TestResolveNeedPwd(t *testing.T) {
	f := withFakeUC(t)
	loginForTest(t)
	f.reply("/api/share/sharepage/token", 200, `{"code":40004,"message":"需要提取码"}`)
	res := Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "code", res.Code, 403)
	mustEqual(t, "msg", res.Msg, "提取码不正确或需要提取码")
	if !res.NeedPwd {
		t.Fatalf("NeedPwd 应为 true")
	}
	// 请求体：share_for_transfer 在前、passcode 为空串
	mustEqual(t, "body", f.reqsFor("/api/share/sharepage/token")[0].Body,
		`{"share_for_transfer":true,"pwd_id":"abc123","passcode":""}`)

	// 文案含 passcode / 密码 也判 need_pwd
	for _, msg := range []string{"passcode invalid", "提取码错误：密码不正确"} {
		f.reply("/api/share/sharepage/token", 200, `{"code":1,"message":`+jsonQuote(msg)+`}`)
		res = Resolve("https://drive.uc.cn/s/abc123", "9999")
		mustEqual(t, "need_pwd msg", res.Msg, "提取码不正确或需要提取码")
		if !res.NeedPwd {
			t.Fatalf("%q 应判 need_pwd", msg)
		}
	}
}

func TestResolveOtherErrors(t *testing.T) {
	f := withFakeUC(t)
	loginForTest(t)

	// 非 JSON → 502
	f.reply("/api/share/sharepage/token", 200, "<html>")
	res := Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "非 JSON", res.Code, 502)
	mustEqual(t, "非 JSON msg", res.Msg, "UC 接口无响应 (HTTP 200)")

	// 业务码非 0 且不是提取码问题 → 403 解析失败
	f.reply("/api/share/sharepage/token", 200, `{"code":5,"message":"分享已失效"}`)
	res = Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "解析失败 code", res.Code, 403)
	mustEqual(t, "解析失败 msg", res.Msg, "解析失败：分享已失效")
	if res.NeedPwd {
		t.Fatalf("不该判 need_pwd")
	}

	// 没有 stoken → 403 未取到 stoken
	f.reply("/api/share/sharepage/token", 200, `{"code":0,"data":{}}`)
	res = Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "无 stoken code", res.Code, 403)
	mustEqual(t, "无 stoken msg", res.Msg, "未取到 stoken")

	// 列表接口失败 → 502
	f.reply("/api/share/sharepage/token", 200, `{"code":0,"data":{"stoken":"ST1"}}`)
	f.reply("/api/share/sharepage/detail", 200, `{"code":7,"message":"目录不存在"}`)
	res = Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "列表失败", res.Code, 502)
	mustEqual(t, "列表失败 msg", res.Msg, "列表获取失败：目录不存在")

	// 列表非 JSON → 502
	f.reply("/api/share/sharepage/detail", 200, "oops")
	res = Resolve("https://drive.uc.cn/s/abc123", "")
	mustEqual(t, "列表非 JSON", res.Code, 502)
	mustEqual(t, "列表非 JSON msg", res.Msg, "UC 列表接口无响应 (HTTP 200)")
}

func TestResolveSuccessWithSubdir(t *testing.T) {
	f := withFakeUC(t)
	loginForTest(t)
	f.reply("/api/share/sharepage/token", 200, `{"code":0,"data":{"stoken":"ST1"}}`)
	f.on("/api/share/sharepage/detail", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(detailJSON(r.URL.Query().Get("pdir_fid"))))
	})

	res := Resolve("https://drive.uc.cn/s/abc123?pwd=8888", "8888")
	mustEqual(t, "code", res.Code, 0)
	mustValue(t, "share_key", res.Data["share_key"], "abc123")
	mustValue(t, "stoken", res.Data["stoken"], "ST1")
	files, ok := res.Data["files"].([]map[string]any)
	if !ok {
		t.Fatalf("files 类型应为 []map[string]any，得到 %T", res.Data["files"])
	}
	mustEqual(t, "files 条数（3 根 + 1 子）", len(files), 4)

	f0 := files[0]
	mustValue(t, "name", f0["name"], "Setup.apk")
	mustValue(t, "size", f0["size"], int64(1048576))
	mustValue(t, "size_mb", f0["size_mb"], float64(1))
	mustValue(t, "isdir", f0["isdir"], false)
	mustValue(t, "fid", f0["fid"], "f1")
	mustValue(t, "fid_token", f0["fid_token"], "t1")
	mustValue(t, "ext", f0["ext"], "apk")
	mustValue(t, "updated", f0["updated"], "1759000000")
	if _, has := f0["parent"]; has {
		t.Fatalf("根项不该有 parent")
	}

	f1 := files[1]
	mustValue(t, "大小写 ext", f1["ext"], "png")
	mustValue(t, "10MB size_mb", f1["size_mb"], float64(10))

	f2 := files[2]
	mustValue(t, "目录 isdir", f2["isdir"], true)
	mustValue(t, "目录 ext", f2["ext"], "")

	f3 := files[3]
	mustValue(t, "子目录展开项", f3["name"], "inner.zip")
	mustValue(t, "parent", f3["parent"], "子目录")
	mustValue(t, "子项 isdir", f3["isdir"], false)
	mustValue(t, "子项 size", f3["size"], int64(2048))

	// 请求形态：token 请求带密码；detail 请求参数顺序逐字
	tokenReqs := f.reqsFor("/api/share/sharepage/token")
	mustEqual(t, "token query", tokenReqs[0].RawQuery, "entry=ft&fr=pc&pr=UCBrowser")
	mustEqual(t, "token body", tokenReqs[0].Body,
		`{"share_for_transfer":true,"pwd_id":"abc123","passcode":"8888"}`)
	mustEqual(t, "token cookie", tokenReqs[0].Header.Get("Cookie"), "ctoken=ct; __puus=p")
	mustEqual(t, "token x-ctoken", tokenReqs[0].Header.Get("X-Ctoken"), "ct")

	details := f.reqsFor("/api/share/sharepage/detail")
	mustEqual(t, "detail 请求次数", len(details), 2)
	mustEqual(t, "detail query 顺序", details[0].RawQuery,
		"pwd_id=abc123&passcode=8888&stoken=ST1&pdir_fid=0&force=0&_page=1&_size=100"+
			"&_fetch_banner=1&_fetch_share=1&_fetch_total=1&entry=ft&fr=pc&pr=UCBrowser")
	mustEqual(t, "子目录 pdir_fid", details[1].RawQuery,
		"pwd_id=abc123&passcode=8888&stoken=ST1&pdir_fid=d1&force=0&_page=1&_size=100"+
			"&_fetch_banner=1&_fetch_share=1&_fetch_total=1&entry=ft&fr=pc&pr=UCBrowser")
}

func TestResolveStopsSubdirAt60(t *testing.T) {
	f := withFakeUC(t)
	loginForTest(t)
	f.reply("/api/share/sharepage/token", 200, `{"code":0,"data":{"stoken":"ST1"}}`)
	// 60 个根项（59 文件 + 1 目录）→ flat 已达 60，目录不再展开。
	var sb strings.Builder
	sb.WriteString(`{"code":0,"data":{"list":[`)
	for i := 0; i < 59; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"file_name":"f%d.txt","size":1,"fid":"f%d","dir":false,"file":true}`, i, i)
	}
	sb.WriteString(`,{"file_name":"dir","fid":"d1","dir":true,"file":false}]}}`)
	root := sb.String()
	f.on("/api/share/sharepage/detail", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pdir_fid") == "d1" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"list":[{"file_name":"never.zip","size":1,"fid":"x","dir":false,"file":true}]}}`))
			return
		}
		_, _ = w.Write([]byte(root))
	})
	res := Resolve("abc12345678", "")
	mustEqual(t, "code", res.Code, 0)
	files := res.Data["files"].([]map[string]any)
	mustEqual(t, "files 上限", len(files), 60)
	if n := len(f.reqsFor("/api/share/sharepage/detail")); n != 1 {
		t.Fatalf("达到 60 后不该再展开子目录，detail 请求 %d 次", n)
	}
}

func TestListDirPdirEmptyUsesZero(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/share/sharepage/detail", 200, `{"code":0,"data":{"list":[]}}`)
	got := listDir("K", "S", "", "ctoken=1", "")
	if got.err != "" {
		t.Fatalf("不该报错: %s", got.err)
	}
	if got.list == nil {
		t.Fatalf("空列表应为空切片而不是 nil")
	}
	mustEqual(t, "pdir 空 → 0",
		f.reqsFor("/api/share/sharepage/detail")[0].RawQuery,
		"pwd_id=K&passcode=&stoken=S&pdir_fid=0&force=0&_page=1&_size=100"+
			"&_fetch_banner=1&_fetch_share=1&_fetch_total=1&entry=ft&fr=pc&pr=UCBrowser")
}

// ---------- downloadUrl / refreshPuus / downloadToTmp ----------

func TestDownloadURL(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/file/download", 200, `{"code":0,"data":[{"download_url":"https://cdn.example.com/a.apk"}]}`)
	got := DownloadURL("K", "ST", "f1", "t1", "ctoken=ct")
	mustEqual(t, "直链", got, "https://cdn.example.com/a.apk")
	mustEqual(t, "body", f.reqsFor("/api/file/download")[0].Body,
		`{"fids":["f1"],"pwd_id":"K","stoken":"ST","fids_token":["t1"]}`)
	mustEqual(t, "query", f.reqsFor("/api/file/download")[0].RawQuery, "entry=ft&fr=pc&pr=UCBrowser")

	// code != 0
	f.reply("/api/file/download", 200, `{"code":9,"message":"no"}`)
	mustEqual(t, "code!=0", DownloadURL("K", "ST", "f1", "t1", "c"), "")
	// data 为空数组
	f.reply("/api/file/download", 200, `{"code":0,"data":[]}`)
	mustEqual(t, "空 data", DownloadURL("K", "ST", "f1", "t1", "c"), "")
	// download_url 为空
	f.reply("/api/file/download", 200, `{"code":0,"data":[{"download_url":""}]}`)
	mustEqual(t, "空直链", DownloadURL("K", "ST", "f1", "t1", "c"), "")
	// 非 JSON
	f.reply("/api/file/download", 200, "x")
	mustEqual(t, "非 JSON", DownloadURL("K", "ST", "f1", "t1", "c"), "")
}

func TestRefreshPuus(t *testing.T) {
	f := withFakeUC(t)
	// 已有 __puus → 就地替换
	f.reply("/api/auth/pc/flush", 200, "", "__puus=new1; Path=/; Secure")
	mustEqual(t, "替换", RefreshPuus("ctoken=ct; __puus=old; a=1"),
		"ctoken=ct; __puus=new1; a=1")
	// 没有 __puus → 追加
	mustEqual(t, "追加", RefreshPuus("ctoken=ct; a=1"), "ctoken=ct; a=1; __puus=new1")
	// 没有 Set-Cookie → 原样
	f.reply("/api/auth/pc/flush", 200, "")
	mustEqual(t, "无 Set-Cookie", RefreshPuus("ctoken=ct"), "ctoken=ct")
	// Set-Cookie 里没有 __puus → 原样
	f.reply("/api/auth/pc/flush", 200, "", "other=1; Path=/")
	mustEqual(t, "无 __puus", RefreshPuus("ctoken=ct"), "ctoken=ct")
	mustEqual(t, "flush query", f.reqsFor("/api/auth/pc/flush")[0].RawQuery,
		"pr=UCBrowser&fr=pc&entry=ft")
}

func TestDownloadToTmpSuccess(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/auth/pc/flush", 200, "", "__puus=p2; Path=/")
	f.reply("/api/file/download", 200,
		`{"code":0,"data":[{"download_url":`+jsonQuote(f.Server.URL+"/cdn/a.apk")+`}]}`)
	f.reply("/cdn/a.apk", 200, "hello-binary")

	path := DownloadToTmp("K", "ST", "f1", "t1", "ctoken=ct")
	if path == "" {
		t.Fatalf("应下载成功")
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if filepath.Dir(path) != TmpDir {
		t.Fatalf("临时文件应在 TmpDir(%q) 下，得到 %q", TmpDir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读临时文件: %v", err)
	}
	mustEqual(t, "内容", string(b), "hello-binary")

	// CDN 请求头：只有 UA / Referer / Cookie，且 cookie 是刷新过 __puus 的
	reqs := f.reqsFor("/cdn/a.apk")
	if len(reqs) != 1 {
		t.Fatalf("CDN 应请求一次，实际 %d", len(reqs))
	}
	mustEqual(t, "ua", reqs[0].Header.Get("User-Agent"), UA)
	mustEqual(t, "referer", reqs[0].Header.Get("Referer"), "https://drive.uc.cn/")
	mustEqual(t, "cookie", reqs[0].Header.Get("Cookie"), "ctoken=ct; __puus=p2")
	// 不带 API 头
	mustEqual(t, "不该带 origin", reqs[0].Header.Get("Origin"), "")
}

func TestDownloadToTmpHTTPError(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/auth/pc/flush", 200, "")
	f.reply("/api/file/download", 200,
		`{"code":0,"data":[{"download_url":`+jsonQuote(f.Server.URL+"/cdn/404.apk")+`}]}`)
	f.reply("/cdn/404.apk", 404, "not found")

	if p := DownloadToTmp("K", "ST", "f1", "t1", "ct=1"); p != "" {
		t.Fatalf("404 应返回空串，得到 %q", p)
	}
	assertDirEmpty(t, TmpDir)
}

func TestDownloadToTmpNoURL(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/auth/pc/flush", 200, "")
	f.reply("/api/file/download", 200, `{"code":1}`)
	if p := DownloadToTmp("K", "ST", "f1", "t1", "ct=1"); p != "" {
		t.Fatalf("拿不到直链应返回空串，得到 %q", p)
	}
	assertDirEmpty(t, TmpDir)
}

func TestDownloadToTmpEmptyBody(t *testing.T) {
	f := withFakeUC(t)
	f.reply("/api/auth/pc/flush", 200, "")
	f.reply("/api/file/download", 200,
		`{"code":0,"data":[{"download_url":`+jsonQuote(f.Server.URL+"/cdn/empty.apk")+`}]}`)
	f.reply("/cdn/empty.apk", 200, "")
	if p := DownloadToTmp("K", "ST", "f1", "t1", "ct=1"); p != "" {
		t.Fatalf("空文件应返回空串，得到 %q", p)
	}
	assertDirEmpty(t, TmpDir)
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("目录 %q 应无残留文件，实际 %d 个", dir, len(ents))
	}
}
