package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 已知答案向量：按 api.php:57-77 s3_sign_v4 的公式用 Python 独立实现算出来的
// （本机没有 PHP，不能直接跑 PHP 对照；父 agent 在生产双跑时可再核对一次）。
// 固定时间 2026-10-05T12:00:00Z、region cn-nb1、AK/SK 均为测试用假值。
const (
	kaAK       = "AKIDEXAMPLE"
	kaSK       = "SECRETEXAMPLE"
	kaKey      = "chat/20261005120000_abcd1234.png"
	kaPayload  = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")
	kaAuthPut  = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261005/cn-nb1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=33a1f1a08ee657ceb8a6d249a4967e5bf651c947e90c8eb99a50115314029b85"
	kaAuthDels = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261005/cn-nb1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=02b4191303b011997d87961eafe41ac79be0d7bca3ff259ecdcb88a931f8e6c7"
)

type caught struct {
	mu      sync.Mutex
	method  string
	path    string
	headers http.Header
	body    []byte
	status  int
	calls   int
}

func (c *caught) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.method, c.path, c.headers, c.body = r.Method, r.URL.Path, r.Header.Clone(), b
		c.calls++
		code := c.status
		c.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	}
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	fixed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	c, err := NewClient(Options{
		Endpoint:  srv.URL,
		Bucket:    "fenglin",
		Region:    "cn-nb1",
		AccessKey: kaAK,
		SecretKey: kaSK,
		PublicURL: "https://fenglin.example.com",
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:       func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPutBytesMatchesPHPKnownAnswer(t *testing.T) {
	got := &caught{}
	srv := httptest.NewServer(got.handler())
	defer srv.Close()
	c := newTestClient(t, srv)

	if err := c.PutBytes(context.Background(), kaKey, "image/png", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPut {
		t.Fatalf("method=%s", got.method)
	}
	// path-style: /<bucket>/<key>
	if want := "/fenglin/" + kaKey; got.path != want {
		t.Fatalf("path=%s want %s", got.path, want)
	}
	if v := got.headers.Get("x-amz-content-sha256"); v != kaPayload {
		t.Fatalf("payload hash=%s", v)
	}
	if v := got.headers.Get("x-amz-date"); v != "20261005T120000Z" {
		t.Fatalf("x-amz-date=%s", v)
	}
	if v := got.headers.Get("Authorization"); v != kaAuthPut {
		t.Fatalf("Authorization 不匹配已知答案:\n got=%s\nwant=%s", v, kaAuthPut)
	}
	// Content-Type 必须发出去，但**不参与签名**（改了它签名仍然有效）
	if v := got.headers.Get("Content-Type"); v != "image/png" {
		t.Fatalf("content-type=%s", v)
	}
	if string(got.body) != "hello" {
		t.Fatalf("body=%q", got.body)
	}
}

func TestPutStreamUnsignedPayload(t *testing.T) {
	got := &caught{}
	srv := httptest.NewServer(got.handler())
	defer srv.Close()
	c := newTestClient(t, srv)

	payload := "UNSIGNED-APK-BYTES"
	if err := c.PutStream(context.Background(), "apk/fengling_20261005_120000.apk",
		"application/vnd.android.package-archive", strings.NewReader(payload), int64(len(payload)), UnsignedPayload); err != nil {
		t.Fatal(err)
	}
	if v := got.headers.Get("x-amz-content-sha256"); v != UnsignedPayload {
		t.Fatalf("x-amz-content-sha256=%s, 期望 UNSIGNED-PAYLOAD", v)
	}
	if !strings.Contains(got.headers.Get("Authorization"), "Signature=") {
		t.Fatalf("缺签名: %s", got.headers.Get("Authorization"))
	}
	if string(got.body) != payload {
		t.Fatalf("body=%q", got.body)
	}
}

func TestDeleteMatchesPHPKnownAnswer(t *testing.T) {
	got := &caught{}
	srv := httptest.NewServer(got.handler())
	defer srv.Close()
	c := newTestClient(t, srv)

	if err := c.Delete(context.Background(), kaKey); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodDelete {
		t.Fatalf("method=%s", got.method)
	}
	if v := got.headers.Get("Authorization"); v != kaAuthDels {
		t.Fatalf("Authorization 不匹配已知答案:\n got=%s\nwant=%s", v, kaAuthDels)
	}
	// 空体：payload hash 是 sha256("")
	if v := got.headers.Get("x-amz-content-sha256"); v != sha256Hex(nil) {
		t.Fatalf("payload hash=%s", v)
	}
}

func TestS3StatusError(t *testing.T) {
	got := &caught{status: http.StatusForbidden}
	srv := httptest.NewServer(got.handler())
	defer srv.Close()
	c := newTestClient(t, srv)

	err := c.PutBytes(context.Background(), kaKey, "image/png", []byte("x"))
	if err == nil {
		t.Fatal("403 必须返回错误")
	}
	if StatusOf(err) != http.StatusForbidden {
		t.Fatalf("StatusOf=%d", StatusOf(err))
	}
	// 目录不存在这类网络错误：StatusOf 必须是 0（PHP curl 拿不到状态码时也是 0）
	if got := StatusOf(context.DeadlineExceeded); got != 0 {
		t.Fatalf("非 HTTP 错误应返回 0, 实得 %d", got)
	}
}

func TestS3URLHelpers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := newTestClient(t, srv)

	key := "chat/a.png"
	url := c.ObjectURL(key)
	if url != "https://fenglin.example.com/chat/a.png" {
		t.Fatalf("ObjectURL=%s", url)
	}
	if got := c.KeyFromURL(url); got != key {
		t.Fatalf("KeyFromURL=%s", got)
	}
	// 非本站地址一律空（清理时不能误删别的对象）
	for _, u := range []string{"", "https://evil.example.com/chat/a.png", "https://fenglin.example.com.evil/x"} {
		if got := c.KeyFromURL(u); got != "" {
			t.Fatalf("KeyFromURL(%q)=%q, 期望空", u, got)
		}
	}
}

func TestNewClientRequiresAllConfig(t *testing.T) {
	_, err := NewClient(Options{Bucket: "b"})
	if err == nil {
		t.Fatal("缺配置必须报错（main 据此拒绝启动）")
	}
	if !strings.Contains(err.Error(), "S3_ACCESS_KEY") {
		t.Fatalf("错误信息应列出缺失键名: %v", err)
	}
}

// 非法对象键（含空格/中文）必须被拒绝，避免「签名路径」与「实际请求路径」不一致。
func TestS3RejectsUnsafeKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.PutBytes(context.Background(), "chat/有 空格.png", "image/png", []byte("x")); err == nil {
		t.Fatal("非法键必须被拒绝")
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
