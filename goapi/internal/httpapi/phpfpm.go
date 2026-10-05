package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/yookoala/gofast"
)

// maxBufferedBody 是「没有 Content-Length（chunked）时必须先读进内存」的请求体上限。
//
// 生产路径上 nginx 是 proxy_request_buffering on，上游一定有 Content-Length，
// 所以这个上限平时用不到；它只在直连 9100（验收脚本正是这么打的）或将来改配置时兜底。
const maxBufferedBody = 16 << 20 // 16MB

// FCGI 是「其余 action 原样透传 PHP」的实现：一个请求一条 FastCGI 连接
// （php-cgi 不擅长 FCGI 多路复用，短连接最稳，也避免连接池把 SSE 长连接错配）。
type FCGI struct {
	cfg     config.Config
	factory gofast.ClientFactory
	log     *slog.Logger
}

// NewFCGI 建客户端工厂（不预先拨号，每次请求才连，PHP-FPM 重启后自动恢复）。
func NewFCGI(cfg config.Config, log *slog.Logger) *FCGI {
	return &FCGI{
		cfg:     cfg,
		factory: gofast.SimpleClientFactory(gofast.SimpleConnFactory(cfg.FPMNetwork, cfg.FPMAddr)),
		log:     log,
	}
}

// Serve 转发请求：状态码 / 响应头 / 响应体逐字透传，且**流式不缓冲**（每次 Write 后 Flush）。
//
// 注意这里刻意不预设任何响应头（CORS 由 PHP 自己发），否则会出现重复头；
// 也不改写/删除任何客户端请求头（尤其 X-Forwarded-For 必须原样带给 PHP）。
func (f *FCGI) Serve(w http.ResponseWriter, r *http.Request) {
	// 加固：Content-Length 缺失（chunked）时先把 body 读进内存再转发。
	// 原因：php-cgi 是按 CONTENT_LENGTH 读 stdin 的，填不上就会把 POST 体当空，
	// 参数静默丢失 —— 这种 bug 在只跑 GET 的双跑对比里根本暴露不出来。
	stdin, bodyLen, tooLarge, err := bufferChunkedBody(r)
	if err != nil {
		f.log.Warn("读取请求体失败", "err", err)
		setCORS(w.Header())
		(&Ctx{W: w, R: r}).Error("读取请求体失败", 1)
		return
	}
	if tooLarge {
		f.log.Warn("chunked 请求体超过缓冲上限", "limit", maxBufferedBody)
		setCORS(w.Header())
		(&Ctx{W: w, R: r}).Error("请求体过大", 1)
		return
	}

	client, err := f.factory()
	if err != nil {
		f.gatewayError(w, "服务器错误: 网关无法连接 PHP-FPM ("+err.Error()+")")
		return
	}
	defer client.Close()

	req := gofast.NewRequest(r)
	req.Params = f.params(r, bodyLen)
	if stdin != nil {
		req.Stdin = stdin
	}

	resp, err := client.Do(req)
	if err != nil {
		f.gatewayError(w, "服务器错误: 网关转发 PHP-FPM 失败 ("+err.Error()+")")
		return
	}

	fw := &flushWriter{ResponseWriter: w}
	if fl, ok := w.(http.Flusher); ok {
		fw.fl = fl
	}
	action := r.URL.Query().Get("action")
	if err := resp.WriteTo(fw, &stderrWriter{log: f.log, action: action}); err != nil {
		f.log.Warn("FastCGI 透传中断", "action", action, "err", err)
	}
}

// bufferChunkedBody 只在 Content-Length 缺失时缓冲请求体，并回传「实际字节数」，
// 供 CONTENT_LENGTH 参数使用。有 Content-Length 时**完全不碰 body**（流式转发，
// 保证 150m 大上传的内存占用与迁移前一致）。
func bufferChunkedBody(r *http.Request) (stdin io.ReadCloser, length int64, tooLarge bool, err error) {
	if r.ContentLength >= 0 {
		return nil, r.ContentLength, false, nil
	}
	if r.Body == nil {
		return nil, 0, false, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxBufferedBody+1))
	_ = r.Body.Close()
	if err != nil {
		return nil, 0, false, err
	}
	if int64(len(buf)) > maxBufferedBody {
		return nil, 0, true, nil
	}
	return io.NopCloser(bytes.NewReader(buf)), int64(len(buf)), false, nil
}

// ClientIP 取客户端 IP，顺序（阶段 2 的限流也用它）：
//
//  1. `X-Real-IP` —— 由我们自己的 nginx 片段设置（`proxy_set_header X-Real-IP $remote_addr`），可信；
//  2. `X-Forwarded-For` —— 客户端自带，**整串原样**（不切第一个），与 PHP `client_ip()` 一致；
//  3. `RemoteAddr` 的 IP。
//
// 之所以不能直接用 RemoteAddr：Go 前面是 nginx，RemoteAddr 恒为 127.0.0.1，
// 而 PHP 侧 fastcgi_param 的 `$remote_addr` 是真实客户端 IP —— 不按上面的顺序取，
// 透传后 PHP 看到的 REMOTE_ADDR 就变了。
func ClientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return v
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}

// params 构造 CGI 参数（gofast 不会自动填，需要自己补齐）。
// contentLength 是「实际要发的 body 长度」（chunked 场景下已经缓冲好真实长度）。
func (f *FCGI) params(r *http.Request, contentLength int64) map[string]string {
	p := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"REDIRECT_STATUS":   "200", // php-cgi 的强制要求
		"SERVER_SOFTWARE":   "goapi",
		"SERVER_PROTOCOL":   r.Proto,
		"SERVER_NAME":       hostOnly(r.Host),
		"SERVER_PORT":       portOf(r.Host, r.TLS != nil),
		"REQUEST_METHOD":    r.Method,
		"REQUEST_URI":       r.URL.RequestURI(),
		"QUERY_STRING":      r.URL.RawQuery,
		"REQUEST_SCHEME":    requestScheme(r),
		"SCRIPT_FILENAME":   f.cfg.PHPScript,
		"SCRIPT_NAME":       f.cfg.PHPScriptName,
		"DOCUMENT_ROOT":     f.cfg.PHPDocRoot,
		"PATH_INFO":         "",
	}
	if r.TLS != nil {
		p["HTTPS"] = "on"
	}
	// REMOTE_ADDR 用 ClientIP（见其注释：不能用 nginx -> Go 的 RemoteAddr）
	p["REMOTE_ADDR"] = ClientIP(r)
	if _, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		p["REMOTE_PORT"] = port
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		p["CONTENT_TYPE"] = ct
	}
	if contentLength >= 0 {
		p["CONTENT_LENGTH"] = strconv.FormatInt(contentLength, 10)
	}
	for k, vs := range r.Header {
		if len(vs) == 0 {
			continue
		}
		key := "HTTP_" + strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
		if key == "HTTP_CONTENT_TYPE" || key == "HTTP_CONTENT_LENGTH" {
			continue
		}
		// 关键：HTTP_ACCEPT_ENCODING / HTTP_AUTHORIZATION / HTTP_X_FORWARDED_FOR 原样转发，
		// 不做任何改写（gzip 与否由 PHP 按 Accept-Encoding 决定；X-Forwarded-For 必须逐字一致）。
		p[key] = strings.Join(vs, ", ")
	}
	return p
}

// gatewayError 是「PHP-FPM 连不上」时的兜底：形态对齐 api.php:11-18 的致命错误响应
// （`{"code":-1,"msg":"…"}`，没有 data 键）与 http_response_code(500)。
func (f *FCGI) gatewayError(w http.ResponseWriter, msg string) {
	f.log.Error("PHP-FPM 透传失败", "err", msg)
	// Go 自己生成的响应同样要带 CORS 三头（PHP 的每个响应都有）；
	// 注意只在这里补：**透传成功**的响应绝不能补，否则会与 PHP 发的重复。
	setCORS(w.Header())
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(phpjson.Marshal(phpjson.New().Set("code", -1).Set("msg", msg)))
}

// flushWriter 每次 Write 之后立刻 Flush：
// 契约要求「透传不缓冲」，而 Go 的 http.ResponseWriter 自带 4KB 缓冲，
// 不 Flush 的话 PHP 侧 flush() 出来的 SSE 事件会攒在 Go 的缓冲里。
type flushWriter struct {
	http.ResponseWriter
	fl http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.ResponseWriter.Write(p)
	if f.fl != nil {
		f.fl.Flush()
	}
	return n, err
}

// stderrWriter 把 PHP 的 stderr 落到结构化日志（不返回给客户端，PHP 也不会）。
type stderrWriter struct {
	log    *slog.Logger
	action string
}

func (s *stderrWriter) Write(p []byte) (int, error) {
	if s.log != nil {
		if msg := strings.TrimSpace(string(p)); msg != "" {
			s.log.Warn("PHP stderr", "action", s.action, "msg", msg)
		}
	}
	return len(p), nil
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func portOf(host string, tls bool) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	if tls {
		return "443"
	}
	return "80"
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

var _ io.Writer = (*stderrWriter)(nil)
