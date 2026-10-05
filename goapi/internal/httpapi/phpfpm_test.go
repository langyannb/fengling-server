package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/config"
)

// ClientIP 的顺序必须是 X-Real-IP -> X-Forwarded-For(整串) -> RemoteAddr。
func TestClientIPOrder(t *testing.T) {
	// 只有 RemoteAddr
	r := httptest.NewRequest("GET", "/api.php", nil)
	r.RemoteAddr = "203.0.113.9:5001"
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("RemoteAddr 兜底失败: %s", got)
	}
	// 有 XFF：整串原样返回（不切第一个，与 PHP client_ip() 一致）
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if got := ClientIP(r); got != "198.51.100.7, 10.0.0.1" {
		t.Fatalf("XFF 应整串返回: %s", got)
	}
	// 有 X-Real-IP：最优先（我们自己的 nginx 设的，可信）
	r.Header.Set("X-Real-IP", "192.0.2.44")
	if got := ClientIP(r); got != "192.0.2.44" {
		t.Fatalf("X-Real-IP 应最优先: %s", got)
	}
}

// REMOTE_ADDR 必须走 ClientIP（否则 Go 前面是 nginx，PHP 只会看到 127.0.0.1）。
func TestParamsRemoteAddrUsesClientIP(t *testing.T) {
	f := NewFCGI(config.Load(), nil)
	r := httptest.NewRequest("GET", "/api.php?action=version", nil)
	r.RemoteAddr = "127.0.0.1:41000"
	r.Header.Set("X-Real-IP", "192.0.2.44")
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	p := f.params(r, 0)
	if p["REMOTE_ADDR"] != "192.0.2.44" {
		t.Fatalf("REMOTE_ADDR=%s", p["REMOTE_ADDR"])
	}
	// X-Forwarded-For 必须原样带给 PHP，一个字都不许改
	if p["HTTP_X_FORWARDED_FOR"] != "198.51.100.7" {
		t.Fatalf("HTTP_X_FORWARDED_FOR=%s", p["HTTP_X_FORWARDED_FOR"])
	}
	if p["CONTENT_LENGTH"] != "0" {
		t.Fatalf("CONTENT_LENGTH=%s", p["CONTENT_LENGTH"])
	}
	if p["SCRIPT_FILENAME"] != "/www/wwwroot/flfxk/api.php" {
		t.Fatalf("SCRIPT_FILENAME=%s", p["SCRIPT_FILENAME"])
	}
}

// 有 Content-Length 时完全不碰 body（流式转发，大上传内存不变）。
func TestBufferChunkedBodyPassthroughWhenContentLengthKnown(t *testing.T) {
	r := httptest.NewRequest("POST", "/api.php?action=login", strings.NewReader("a=1"))
	stdin, n, tooLarge, err := bufferChunkedBody(r)
	if err != nil || tooLarge {
		t.Fatalf("err=%v tooLarge=%v", err, tooLarge)
	}
	if stdin != nil {
		t.Fatal("有 Content-Length 时不应缓冲")
	}
	if n != 3 {
		t.Fatalf("length=%d", n)
	}
	// 原始 body 仍然可读（没被消费）
	rest, _ := io.ReadAll(r.Body)
	if string(rest) != "a=1" {
		t.Fatalf("body 被消费了: %q", rest)
	}
}

// Content-Length 缺失（chunked）时必须缓冲，并用真实长度回填 CONTENT_LENGTH。
func TestBufferChunkedBodyBuffersAndFixesLength(t *testing.T) {
	r := httptest.NewRequest("POST", "/api.php?action=login", strings.NewReader("user=abc&pass=def"))
	r.ContentLength = -1 // 模拟 chunked
	r.Header.Del("Content-Length")

	stdin, n, tooLarge, err := bufferChunkedBody(r)
	if err != nil || tooLarge {
		t.Fatalf("err=%v tooLarge=%v", err, tooLarge)
	}
	if stdin == nil {
		t.Fatal("chunked 必须缓冲")
	}
	if n != int64(len("user=abc&pass=def")) {
		t.Fatalf("length=%d", n)
	}
	got, _ := io.ReadAll(stdin)
	if string(got) != "user=abc&pass=def" {
		t.Fatalf("缓冲内容=%q", got)
	}
	// 参数里必须填真实长度
	f := NewFCGI(config.Load(), nil)
	p := f.params(r, n)
	if p["CONTENT_LENGTH"] != "17" { // len("user=abc&pass=def") == 17
		t.Fatalf("CONTENT_LENGTH=%s", p["CONTENT_LENGTH"])
	}
}

// 超过上限必须判定为 tooLarge（由调用方回 400 请求体过大）。
func TestBufferChunkedBodyTooLarge(t *testing.T) {
	big := strings.NewReader(strings.Repeat("x", maxBufferedBody+10))
	r := httptest.NewRequest("POST", "/api.php?action=upload", big)
	r.ContentLength = -1
	r.Header.Del("Content-Length")

	_, _, tooLarge, err := bufferChunkedBody(r)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !tooLarge {
		t.Fatal("应判定为请求体过大")
	}
}

var _ = http.StatusOK
