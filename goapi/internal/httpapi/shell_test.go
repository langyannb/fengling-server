package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestCtx(t *testing.T, method, target, body string, headers map[string]string) (*Ctx, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	env := &Env{}
	return env.newCtx(w, r, "test"), w
}

func TestJSONShellIsByteExact(t *testing.T) {
	c, w := newTestCtx(t, "GET", "/api.php?action=x", "", nil)
	c.JSON(nil, 1, "用户名或密码错误", 0)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
	if got, want := w.Body.String(), `{"code":1,"msg":"用户名或密码错误","data":null}`; got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type=%s", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control=%s", got)
	}
}

func TestJSONShellDataNullVsEmpty(t *testing.T) {
	c, w := newTestCtx(t, "GET", "/api.php?action=x", "", nil)
	c.JSON(nil, 0, "ok", 0)
	if got, want := w.Body.String(), `{"code":0,"msg":"ok","data":null}`; got != want {
		t.Fatalf("null: got %s", got)
	}
	c2, w2 := newTestCtx(t, "GET", "/api.php?action=x", "", nil)
	c2.JSON([]any{}, 0, "ok", 0)
	if got, want := w2.Body.String(), `{"code":0,"msg":"ok","data":[]}`; got != want {
		t.Fatalf("empty array: got %s", got)
	}
}

func TestStatusCodeMappingMatchesPHP(t *testing.T) {
	cases := []struct {
		code int
		want int
	}{
		{0, 200}, {1, 400}, {401, 401}, {403, 400}, {404, 404}, {500, 500}, {1001, 400},
	}
	for _, tc := range cases {
		c, w := newTestCtx(t, "GET", "/api.php?action=x", "", nil)
		c.Error("x", tc.code)
		if w.Code != tc.want {
			t.Errorf("code=%d http=%d want %d", tc.code, w.Code, tc.want)
		}
		// JSON 体里的 code 始终是业务码
		wantBody := `"code":` + itoa(tc.code) + `,`
		if !strings.Contains(w.Body.String(), wantBody) {
			t.Errorf("code=%d body=%s 缺 %s", tc.code, w.Body.String(), wantBody)
		}
	}
}

func TestErrorAlwaysNullData(t *testing.T) {
	c, w := newTestCtx(t, "GET", "/api.php?action=stream", "", nil)
	c.Error("登录已失效", 401)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
	if got, want := w.Body.String(), `{"code":401,"msg":"登录已失效","data":null}`; got != want {
		t.Fatalf("got %s", got)
	}
}

func TestCacheSecondsHeader(t *testing.T) {
	c, w := newTestCtx(t, "GET", "/api.php?action=banners", "", nil)
	c.JSON([]any{}, 0, "ok", 300)
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("cache-control=%s", got)
	}
}

func TestGzipOnlyWhenAcceptEncodingHasGzip(t *testing.T) {
	// 不带 Accept-Encoding：明文
	c, w := newTestCtx(t, "GET", "/api.php?action=x", "", nil)
	c.JSON(nil, 0, "ok", 0)
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatal("不应压缩")
	}
	// 带 Accept-Encoding: gzip：压缩，且解压后与明文一致
	c2, w2 := newTestCtx(t, "GET", "/api.php?action=x", "", map[string]string{"Accept-Encoding": "gzip, deflate"})
	c2.JSON(nil, 0, "ok", 0)
	if w2.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("应压缩")
	}
	zr, err := gzip.NewReader(bytes.NewReader(w2.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"code":0,"msg":"ok","data":null}` {
		t.Fatalf("解压后 %s", plain)
	}
	// PHP gzencode 的 OS 字节是 3(Unix)，Go 默认 255；第 9 字节必须是 3。
	if got := w2.Body.Bytes()[9]; got != 3 {
		t.Fatalf("gzip OS byte=%d, 期望 3", got)
	}
}

func TestParamOrderMatchesPHP(t *testing.T) {
	// JSON body > $_POST > $_GET
	c, _ := newTestCtx(t, "POST", "/api.php?action=x&k=fromquery", `{"k":"fromjson"}`,
		map[string]string{"Content-Type": "application/json"})
	if got := c.ParamStr("k", ""); got != "fromjson" {
		t.Fatalf("json 优先失效: %s", got)
	}
	// 表单 body + 查询串 → 表单优先
	c2, _ := newTestCtx(t, "POST", "/api.php?action=x&k=fromquery", "k=fromform",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if got := c2.ParamStr("k", ""); got != "fromform" {
		t.Fatalf("表单优先失效: %s", got)
	}
	// 只有查询串
	c3, _ := newTestCtx(t, "GET", "/api.php?action=x&k=fromquery", "", nil)
	if got := c3.ParamStr("k", ""); got != "fromquery" {
		t.Fatalf("查询串失效: %s", got)
	}
}

func TestTokenFromHeaderThenQuery(t *testing.T) {
	c, _ := newTestCtx(t, "GET", "/api.php?action=stream&token=qtok", "", map[string]string{"Authorization": "bearer  htok"})
	if got := c.Token(); got != "htok" {
		t.Fatalf("Bearer 优先失效: %q", got)
	}
	c2, _ := newTestCtx(t, "GET", "/api.php?action=stream&token=qtok", "", nil)
	if got := c2.Token(); got != "qtok" {
		t.Fatalf("查询串 token 失效: %q", got)
	}
}

func TestPHPCasts(t *testing.T) {
	if got := atoiPrefix(" 12abc"); got != 12 {
		t.Errorf("(int)' 12abc'=%d", got)
	}
	if got := atoiPrefix("abc"); got != 0 {
		t.Errorf("(int)'abc'=%d", got)
	}
	if got := atoiPrefix(""); got != 0 {
		t.Errorf("(int)''=%d", got)
	}
	if got := atoiPrefix("-7x"); got != -7 {
		t.Errorf("(int)'-7x'=%d", got)
	}
	if got := floatPrefix("12.5abc"); got != 12.5 {
		t.Errorf("(float)'12.5abc'=%v", got)
	}
	if got := floatPrefix("abc"); got != 0 {
		t.Errorf("(float)'abc'=%v", got)
	}
	if got := phpInt(nil); got != 0 {
		t.Errorf("(int)null=%d", got)
	}
	if got := phpInt(true); got != 1 {
		t.Errorf("(int)true=%d", got)
	}
	if got := phpFloat("3"); got != 3 {
		t.Errorf("(float)'3'=%v", got)
	}
}

func TestOptionsIs204WithCORS(t *testing.T) {
	rt := &Router{env: Env{}}
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("OPTIONS", "/api.php?action=version", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("cors=%s", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Fatalf("methods=%s", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
