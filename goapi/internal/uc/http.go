package uc

import (
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 包级可替换配置。默认值与 PHP 逐字一致；接线与测试里替换。
//
//	APIBase  ↔ self::API（uc.php:16）
//	CASBase  ↔ self::CAS（uc.php:17）
//	DriveBase↔ ticketToCookie() 里写死的 https://drive.uc.cn（uc.php:227-231）
//	           仅为了可测而提出成变量，默认值不变，最终 URL 与 PHP 相同。
//	AuthPath ↔ self::authFile() = __DIR__ . '/uc_auth.php'（uc.php:20-23）
//	           默认相对名，生产由接线方设为 PHPDocRoot/uc_auth.php。
//	TmpDir   ↔ sys_get_temp_dir()（uc.php:461）
//
// 这些是变量而不是常量：测试必须能把它们指向 httptest 服务/临时目录。
var (
	APIBase   = "https://pc-api.uc.cn/1/clouddrive"
	CASBase   = "https://api.open.uc.cn/cas/ajax"
	DriveBase = "https://drive.uc.cn"
	AuthPath  = "uc_auth.php"
	TmpDir    = os.TempDir()
)

const (
	// UA 对齐 uc.php:15 的 UcDrive::UA。
	UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	// ClientID 对齐 uc.php:18 的 UcDrive::CLIENT_ID。
	ClientID = "381"
)

// httpTransport 对齐 curl 的 CURLOPT_SSL_VERIFYPEER=false / CURLOPT_SSL_VERIFYHOST=0。
// UC 的 CDN 证书链在部分服务器上不完整，PHP 侧一直是关校验的，这里照抄。
var httpTransport = &http.Transport{
	TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 对齐 PHP 行为
	MaxIdleConns:      16,
	IdleConnTimeout:   30 * time.Second,
	DisableKeepAlives: false,
	// 对齐 CURLOPT_ENCODING = ''：自动协商并解压。
	DisableCompression: false,
}

// httpResult 对齐 req() 返回的数组（uc.php:87-97）。
type httpResult struct {
	ok         bool
	status     int
	body       string
	setCookies []string
	location   string
	errText    string
}

// reqOpt 对齐 req() 的 $opt（uc.php:61-79）。
type reqOpt struct {
	method  string
	headers []string
	body    string
	cookie  string
	timeout time.Duration
}

// req 对齐 UcDrive::req()：返回状态码、body、全部 Set-Cookie、Location。
//
// PHP 是 CURLOPT_HEADER=true 拿整段原始响应再切；Go 直接读 Header，语义等价：
//   - 不跟随重定向（CURLOPT_FOLLOWLOCATION=false）→ ErrUseLastResponse；
//   - CURLOPT_TIMEOUT 默认 30s，CURLOPT_CONNECTTIMEOUT=10s（Go 的 client.Timeout
//     覆盖整次请求，比分别设 dial 更接近 curl 的总超时语义）。
func req(rawURL string, opt reqOpt) httpResult {
	method := opt.method
	if method == "" {
		method = "GET"
	}
	timeout := opt.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var body io.Reader
	if opt.body != "" {
		body = strings.NewReader(opt.body)
	}
	hr, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return httpResult{ok: false, errText: err.Error()}
	}
	for _, h := range opt.headers {
		name, value, found := strings.Cut(h, ":")
		if !found {
			continue
		}
		hr.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	// 对齐 `if (!empty($opt['cookie'])) curl_setopt($ch, CURLOPT_COOKIE, $opt['cookie'])`。
	if opt.cookie != "" {
		hr.Header.Set("Cookie", opt.cookie)
	}
	client := &http.Client{
		Transport: httpTransport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(hr)
	if err != nil {
		return httpResult{ok: false, errText: err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpResult{ok: false, errText: err.Error()}
	}
	sc := resp.Header.Values("Set-Cookie")
	cookies := make([]string, 0, len(sc))
	for _, c := range sc {
		cookies = append(cookies, strings.TrimSpace(c))
	}
	return httpResult{
		ok:         true,
		status:     resp.StatusCode,
		body:       string(raw),
		setCookies: cookies,
		location:   strings.TrimSpace(resp.Header.Get("Location")),
	}
}

// cookieJar 对齐 PHP 手写 jar（uc.php:100-118）：
//   - absorb() 取每个 Set-Cookie 第一个 ';' 前的 k=v，按 name 覆盖；
//   - 同名覆盖时保留首次插入的位置（PHP 数组赋值语义）；
//   - jarStr() 按插入序拼 "k=v; k=v"。
type cookieJar struct {
	keys []string
	vals map[string]string
}

func newCookieJar() *cookieJar {
	return &cookieJar{vals: map[string]string{}}
}

// absorb 对齐 UcDrive::absorb()。
func (j *cookieJar) absorb(setCookies []string) {
	for _, c := range setCookies {
		main := strings.TrimSpace(strings.SplitN(c, ";", 2)[0])
		p := strings.IndexByte(main, '=')
		if p > 0 {
			k := strings.TrimSpace(main[:p])
			v := strings.TrimSpace(main[p+1:])
			if k != "" {
				if _, seen := j.vals[k]; !seen {
					j.keys = append(j.keys, k)
				}
				j.vals[k] = v
			}
		}
	}
}

// String 对齐 UcDrive::jarStr()。
func (j *cookieJar) String() string {
	out := make([]string, 0, len(j.keys))
	for _, k := range j.keys {
		out = append(out, k+"="+j.vals[k])
	}
	return strings.Join(out, "; ")
}

var ctokenRe = regexp.MustCompile(`(?:^|;\s*)ctoken=([^;]+)`)

// ctoken 对齐 UcDrive::ctoken()（uc.php:120-124）。
func ctoken(cookie string) string {
	m := ctokenRe.FindStringSubmatch(cookie)
	if m == nil {
		return ""
	}
	return m[1]
}

// casHeaders 对齐 UcDrive::casHeaders()，顺序逐字（uc.php:126-135）。
func casHeaders() []string {
	return []string{
		"accept: application/json, text/plain, */*",
		"content-type: application/x-www-form-urlencoded",
		"origin: https://drive.uc.cn",
		"referer: https://drive.uc.cn/",
		"user-agent: " + UA,
	}
}

// apiHeaders 对齐 UcDrive::apiHeaders()，顺序逐字（uc.php:137-155）。
func apiHeaders(cookie string) []string {
	h := []string{
		"accept: application/json, text/plain, */*",
		"accept-language: zh-CN,zh;q=0.9,en;q=0.8",
		"content-type: application/json;charset=UTF-8",
		"origin: https://drive.uc.cn",
		"referer: https://drive.uc.cn/",
		"sec-fetch-dest: empty",
		"sec-fetch-mode: cors",
		"sec-fetch-site: same-site",
		"user-agent: " + UA,
	}
	if cookie != "" {
		if ct := ctoken(cookie); ct != "" {
			h = append(h, "x-ctoken: "+ct)
		}
	}
	return h
}

// ts 对齐 UcDrive::ts()：(string)(int)(microtime(true) * 1000)。
func ts() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

// apiURL 拼 API 路径（等价于 self::API . '/xxx'）。
func apiURL(pathAndQuery string) string {
	return APIBase + pathAndQuery
}

// casURL 拼 CAS 路径（等价于 self::CAS . '/xxx'）。
func casURL(pathAndQuery string) string {
	return CASBase + pathAndQuery
}
