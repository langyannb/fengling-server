package uc

import (
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 本文件对齐 uc.php 的「分享解析」段（uc.php:299-482）。

var (
	shareKeyPathRe  = regexp.MustCompile(`/s/([a-zA-Z0-9]+)`)
	shareKeyParamRe = regexp.MustCompile(`(?:pwd_id|share_id)=([a-zA-Z0-9]+)`)
	shareKeyBareRe  = regexp.MustCompile(`^[a-zA-Z0-9]{8,}$`)
)

// ShareKey 对齐 UcDrive::shareKey()（uc.php:301-308）。
//
//	/s/XXXX           → XXXX
//	?pwd_id=XXXX      → XXXX（share_id= 同）
//	纯 8 位以上字母数字 → 原样
func ShareKey(shareURL string) string {
	u := strings.TrimSpace(shareURL)
	if m := shareKeyPathRe.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	if m := shareKeyParamRe.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	if shareKeyBareRe.MatchString(u) {
		return u
	}
	return ""
}

// Resolve 对齐 UcDrive::resolve()（uc.php:310-359）。
//
// 错误码谱系（api.php:3130 据此映射 HTTP）：
//
//	401 未登录 → HTTP 401
//	400 认不出链接 → HTTP 400
//	403 need_pwd=true（提取码不对/需要提取码）→ HTTP 400
//	403 其它（解析失败 / 未取到 stoken）→ HTTP 502
//	502 接口无响应 / 列表失败 → HTTP 502
//
// 成功时 Data: share_key / stoken / files（[]map[string]any）。
func Resolve(shareURL, pwd string) Result {
	auth := LoadAuth()
	if auth.Cookie == "" || auth.Cookie == "0" {
		return Result{Code: 401, Msg: "UC 账号未登录，请先在后台扫码登录"}
	}
	key := ShareKey(shareURL)
	if key == "" {
		return Result{Code: 400, Msg: "无法识别 UC 分享链接"}
	}
	cookie := auth.Cookie

	body := `{"share_for_transfer":true,"pwd_id":` + jsonQuote(key) +
		`,"passcode":` + jsonQuote(pwd) + `}`
	r := req(apiURL("/share/sharepage/token?entry=ft&fr=pc&pr=UCBrowser"), reqOpt{
		method:  "POST",
		cookie:  cookie,
		headers: apiHeaders(cookie),
		body:    body,
	})
	j, isArr := decodeArray(r.body)
	if !isArr {
		return Result{Code: 502, Msg: "UC 接口无响应 (HTTP " + strconv.Itoa(r.status) + ")"}
	}
	code := int64(-1)
	if isset(j, "code") {
		code = phpInt(j["code"])
	}
	if code != 0 {
		msg := "未知错误"
		if isset(j, "message") {
			msg = phpStr(j["message"])
		}
		lower := strings.ToLower(msg)
		if code == 40004 || strings.Contains(lower, "passcode") || strings.Contains(msg, "密码") {
			return Result{Code: 403, Msg: "提取码不正确或需要提取码", NeedPwd: true}
		}
		return Result{Code: 403, Msg: "解析失败：" + msg}
	}
	stoken := ""
	if isset(j, "data") {
		d := asMap(j["data"])
		if isset(d, "stoken") {
			stoken = phpStr(d["stoken"])
		}
	}
	if stoken == "" {
		return Result{Code: 403, Msg: "未取到 stoken"}
	}

	root := listDir(key, stoken, "0", cookie, pwd)
	if root.err != "" {
		return Result{Code: 502, Msg: root.err}
	}
	flat := make([]map[string]any, 0, len(root.list))
	for _, f := range root.list {
		flat = append(flat, normFile(f))
	}
	// 一级子目录展开：仅对 dir 非空、且总数还没到 60 的项展开一次。
	// count($flat) < 60 是在循环里动态判的，展开出来的项同样会占额度。
	for _, f := range root.list {
		if phpEmpty(f["dir"]) || len(flat) >= 60 {
			continue
		}
		sub := listDir(key, stoken, phpStr(f["fid"]), cookie, pwd)
		if sub.err != "" {
			continue
		}
		for _, sf := range sub.list {
			n := normFile(sf)
			n["parent"] = phpStr(f["file_name"])
			flat = append(flat, n)
		}
	}
	return Result{Code: 0, Data: map[string]any{
		"share_key": key,
		"stoken":    stoken,
		"files":     flat,
	}}
}

// dirList 对齐 listDir() 的返回：['list'=>..., 'raw'=>...] 或 ['error'=>...]。
type dirList struct {
	list []map[string]any
	err  string
}

// listDir 对齐 UcDrive::listDir()（uc.php:361-388）。
//
// 参数顺序就是 http_build_query 的数组顺序，UC 对参数序敏感，别改。
func listDir(key, stoken, pdir, cookie, pwd string) dirList {
	pdirFid := pdir
	if pdir == "" {
		pdirFid = "0"
	}
	qs := phpBuildQuery([][2]string{
		{"pwd_id", key},
		{"passcode", pwd},
		{"stoken", stoken},
		{"pdir_fid", pdirFid},
		{"force", "0"},
		{"_page", "1"},
		{"_size", "100"},
		{"_fetch_banner", "1"},
		{"_fetch_share", "1"},
		{"_fetch_total", "1"},
		{"entry", "ft"},
		{"fr", "pc"},
		{"pr", "UCBrowser"},
	})
	r := req(apiURL("/share/sharepage/detail?"+qs), reqOpt{
		method:  "GET",
		cookie:  cookie,
		headers: apiHeaders(cookie),
	})
	j, isArr := decodeArray(r.body)
	if !isArr {
		return dirList{err: "UC 列表接口无响应 (HTTP " + strconv.Itoa(r.status) + ")"}
	}
	code := int64(-1)
	if isset(j, "code") {
		code = phpInt(j["code"])
	}
	if code != 0 {
		msg := "未知"
		if isset(j, "message") {
			msg = phpStr(j["message"])
		}
		return dirList{err: "列表获取失败：" + msg}
	}
	out := make([]map[string]any, 0)
	for _, item := range asList(asMap(j["data"])["list"]) {
		out = append(out, asMap(item))
	}
	return dirList{list: out}
}

// normFile 对齐 UcDrive::normFile()（uc.php:390-402）：字段名与顺序逐字。
func normFile(f map[string]any) map[string]any {
	name := ""
	if isset(f, "file_name") {
		name = phpStr(f["file_name"])
	}
	size := int64(0)
	var sizeMB any = int64(0)
	if isset(f, "size") {
		size = phpInt(f["size"])
		sizeMB = phpRound2(float64(size) / 1048576)
	}
	fid := ""
	if isset(f, "fid") {
		fid = phpStr(f["fid"])
	}
	fidToken := ""
	if isset(f, "share_fid_token") {
		fidToken = phpStr(f["share_fid_token"])
	}
	updated := ""
	if isset(f, "updated_at") {
		updated = phpStr(f["updated_at"])
	}
	return map[string]any{
		"name":      name,
		"size":      size,
		"size_mb":   sizeMB,
		"isdir":     isDirEntry(f),
		"fid":       fid,
		"fid_token": fidToken,
		"ext":       strings.ToLower(pathinfoExt(name)),
		"updated":   updated,
	}
}

// isDirEntry 对齐 UcDrive::isDirEntry()（uc.php:404-411）：
// dir/file 字段优先（存在任一就以它们为准），否则 file_type===1 视为目录。
func isDirEntry(f map[string]any) bool {
	_, hasDir := f["dir"]
	_, hasFile := f["file"]
	if hasDir || hasFile {
		return !phpEmpty(f["dir"]) && phpEmpty(f["file"])
	}
	ft := int64(0)
	if isset(f, "file_type") {
		ft = phpInt(f["file_type"])
	}
	return ft == 1
}

// DownloadURL 对齐 UcDrive::downloadUrl()（uc.php:413-433）：
// 取分享内文件的直链，失败返回 ""。
func DownloadURL(key, stoken, fid, fidToken, cookie string) string {
	body := `{"fids":[` + jsonQuote(fid) + `],"pwd_id":` + jsonQuote(key) +
		`,"stoken":` + jsonQuote(stoken) + `,"fids_token":[` + jsonQuote(fidToken) + `]}`
	r := req(apiURL("/file/download?entry=ft&fr=pc&pr=UCBrowser"), reqOpt{
		method:  "POST",
		cookie:  cookie,
		headers: apiHeaders(cookie),
		body:    body,
	})
	j, isArr := decodeArray(r.body)
	if !isArr {
		return ""
	}
	code := int64(-1)
	if isset(j, "code") {
		code = phpInt(j["code"])
	}
	if code != 0 {
		return ""
	}
	data := asList(j["data"])
	if len(data) == 0 {
		return ""
	}
	first := asMap(data[0])
	if phpEmpty(first["download_url"]) {
		return ""
	}
	return phpStr(first["download_url"])
}

var (
	puusInSetCookieRe = regexp.MustCompile(`__puus=([^;]+)`)
	puusInCookieRe    = regexp.MustCompile(`__puus=[^;]+`)
)

// RefreshPuus 对齐 UcDrive::refreshPuus()（uc.php:435-453）：
// OSS 直链回调校验必须带 __puus，否则 403 RequestDeniedByCallback。
func RefreshPuus(cookie string) string {
	r := req(apiURL("/auth/pc/flush?pr=UCBrowser&fr=pc&entry=ft"), reqOpt{
		method:  "GET",
		cookie:  cookie,
		headers: apiHeaders(cookie),
	})
	if len(r.setCookies) == 0 {
		return cookie
	}
	puus := ""
	for _, one := range r.setCookies {
		if m := puusInSetCookieRe.FindStringSubmatch(one); m != nil {
			puus = m[1]
			break
		}
	}
	if puus == "" {
		return cookie
	}
	if puusInCookieRe.MatchString(cookie) {
		return puusInCookieRe.ReplaceAllStringFunc(cookie, func(string) string {
			return "__puus=" + puus
		})
	}
	return cookie + "; __puus=" + puus
}

// DownloadToTmp 对齐 UcDrive::downloadToTmp()（uc.php:455-482）：
// 流式下载到 TmpDir 下的临时文件，成功返回路径，失败返回 ""（并删掉半截文件）。
func DownloadToTmp(key, stoken, fid, fidToken, cookie string) string {
	ck := RefreshPuus(cookie)
	dl := DownloadURL(key, stoken, fid, fidToken, ck)
	if dl == "" {
		return ""
	}
	dir := TmpDir
	if dir == "" {
		dir = "/tmp"
	}
	f, err := os.CreateTemp(dir, "ucf_*")
	if err != nil {
		return ""
	}
	name := f.Name()
	status, err := fetchToFile(dl, ck, f)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil || status < 200 || status >= 300 {
		_ = os.Remove(name)
		return ""
	}
	if st, serr := os.Stat(name); serr != nil || st.Size() == 0 {
		_ = os.Remove(name)
		return ""
	}
	return name
}

// fetchToFile 对齐 curl 的 CURLOPT_FILE 流式落盘：
// 跟随重定向（上限 5 次）、总超时 600s、不走 API 头（只带 UA/Referer/Cookie）。
func fetchToFile(dl, cookie string, w io.Writer) (int, error) {
	hr, err := http.NewRequest("GET", dl, nil)
	if err != nil {
		return 0, err
	}
	hr.Header.Set("user-agent", UA)
	hr.Header.Set("referer", "https://drive.uc.cn/")
	if cookie != "" {
		hr.Header.Set("cookie", cookie)
	}
	client := &http.Client{
		Transport: httpTransport,
		Timeout:   600 * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			// 对齐 CURLOPT_MAXREDIRS = 5。
			if len(via) > 5 {
				return errors.New("超过最大重定向次数")
			}
			return nil
		},
	}
	resp, err := client.Do(hr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(w, resp.Body); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

var (
	gvExtRe   = regexp.MustCompile(`\.[a-zA-Z0-9]{2,5}$`)
	gvVerRe   = regexp.MustCompile(`(?i)v?(\d+(?:\.\d+){1,3}(?:[-_][A-Za-z0-9.]+)?)`)
	gvSpaceRe = regexp.MustCompile(`[\s_\-]+`)
	gvBracket = strings.NewReplacer("[", " ", "]", " ", "(", " ", ")", " ")
)

// GuessNameVersion 对齐 UcDrive::guessNameVersion()（uc.php:484-501）：
// 从文件名猜「软件名 / 版本号」，前端用它预填表单。
//
// 步骤逐字照抄：去末尾扩展名 → []() 变空格 → 提版本（可带 v 前缀）→
// 把 "v版本" 与 "版本" 从名字里剔掉 → 合并空白/下划线/连字符 → 首尾 trim " -_"。
func GuessNameVersion(fileName string) (name, version string) {
	base := gvExtRe.ReplaceAllString(fileName, "")
	base = gvBracket.Replace(base)
	ver := ""
	if m := gvVerRe.FindStringSubmatch(base); m != nil {
		ver = m[1]
	}
	name = base
	if ver != "" {
		name = strings.ReplaceAll(name, "v"+ver, " ")
		name = strings.ReplaceAll(name, ver, " ")
	}
	name = gvSpaceRe.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	name = strings.Trim(name, " -_")
	if name == "" {
		name = base
	}
	return name, ver
}
