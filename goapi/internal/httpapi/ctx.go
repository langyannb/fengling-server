package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/realtime"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// Env 是 Router 的依赖集合（main.go 组装一次，全程只读）。
type Env struct {
	Cfg    config.Config
	Store  *store.Store
	Redis  *store.Redis
	Engine *realtime.Engine
	Log    *slog.Logger

	Version   string    // -ldflags -X main.buildVersion
	BuildTime string    // -ldflags -X main.buildTime
	Started   time.Time // 进程启动时刻（health 的 uptime_sec）
}

// Ctx 是一次原生 action 的上下文。
//
// 参数读取刻意复刻 PHP：api.php 用 $_GET['action'] 路由，param() 的顺序是
// request_body()（php://input 里的 JSON）→ $_POST → $_GET（config.php:27-44）。
type Ctx struct {
	W      http.ResponseWriter
	R      *http.Request
	Action string
	Env    *Env

	Query url.Values

	raw      []byte // 请求体原文（只读一份，JSON 与表单两种解析共用）
	bodyOnce sync.Once
	body     map[string]any
	formOnce sync.Once
	form     url.Values
}

const maxBodyBytes = 8 << 20

func (e *Env) newCtx(w http.ResponseWriter, r *http.Request, action string) *Ctx {
	c := &Ctx{W: w, R: r, Action: action, Env: e, Query: r.URL.Query()}
	// 原生 action 从不转发请求体，所以这里可以先读一份原文（透传路径完全不碰它）。
	if r.Body != nil {
		if raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes)); err == nil {
			c.raw = raw
		}
	}
	return c
}

// Log 返回带 action 的 logger。
func (c *Ctx) Log() *slog.Logger {
	return c.Env.Log.With("action", c.Action)
}

// Body 复刻 config.php:27 request_body()：对 php://input 做 json_decode($raw, true) ?: []。
func (c *Ctx) Body() map[string]any {
	c.bodyOnce.Do(func() {
		if len(c.raw) == 0 {
			return
		}
		dec := json.NewDecoder(bytes.NewReader(c.raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return
		}
		c.body = m
	})
	return c.body
}

// PostForm 对应 PHP 的 $_POST（仅表单类型请求体）。
func (c *Ctx) PostForm() url.Values {
	c.formOnce.Do(func() {
		ct := c.R.Header.Get("Content-Type")
		if len(c.raw) == 0 || !strings.Contains(ct, "application/x-www-form-urlencoded") {
			return
		}
		vs, err := url.ParseQuery(string(c.raw))
		if err != nil {
			return
		}
		c.form = vs
	})
	return c.form
}

// Param 复刻 config.php:37 param()：JSON body → $_POST → $_GET。
func (c *Ctx) Param(key string) (any, bool) {
	if b := c.Body(); b != nil {
		if v, ok := b[key]; ok {
			return v, true
		}
	}
	if f := c.PostForm(); f != nil {
		if vs, ok := f[key]; ok && len(vs) > 0 {
			return vs[0], true
		}
	}
	if vs, ok := c.Query[key]; ok && len(vs) > 0 {
		return vs[0], true
	}
	return nil, false
}

// ParamStr 取字符串参数（缺省返回 def）。
func (c *Ctx) ParamStr(key, def string) string {
	v, ok := c.Param(key)
	if !ok {
		return def
	}
	return phpStr(v)
}

// ParamInt 取整型参数，使用 PHP 的 (int) 强转语义（缺省返回 def）。
func (c *Ctx) ParamInt(key string, def int64) int64 {
	v, ok := c.Param(key)
	if !ok {
		return def
	}
	return phpInt(v)
}

// bearerRe 对齐 api.php:3267 的 preg_match('/Bearer\s+(\S+)/i', $auth)。
var bearerRe = regexp.MustCompile(`(?i)Bearer\s+(\S+)`)

// Token 对齐 api.php:3350-3356（current_user）：Authorization: Bearer 优先，否则 param('token')。
func (c *Ctx) Token() string {
	if m := bearerRe.FindStringSubmatch(c.R.Header.Get("Authorization")); len(m) == 2 {
		return m[1]
	}
	if v, ok := c.Param("token"); ok {
		return phpStr(v)
	}
	return ""
}

// QueryToken 只从查询串/Authorization 头取 token（WebSocket 用，不解析请求体）。
func QueryToken(r *http.Request) string {
	if m := bearerRe.FindStringSubmatch(r.Header.Get("Authorization")); len(m) == 2 {
		return m[1]
	}
	return r.URL.Query().Get("token")
}

// ---------- PHP 标量强转（原生 action 与 PHP 的输出必须逐字一致，所以不能直接用 Go 的解析） ----------

// phpInt 复刻 PHP 的 (int)："(int)12abc" == 12、"(int)abc" == 0。
func phpInt(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return int64(f)
		}
		return 0
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		return atoiPrefix(x)
	}
	return 0
}

// atoiPrefix 复刻 PHP 的 (int)"..." 前缀解析（十进制，允许前后空白与正负号）。
func atoiPrefix(s string) int64 {
	s = strings.TrimSpace(s)
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == i {
		return 0
	}
	n, err := strconv.ParseInt(s[:j], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// phpFloat 复刻 PHP 的 (float)。
func phpFloat(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		return floatPrefix(x)
	}
	return 0
}

var floatPrefixRe = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`)

// floatPrefix 复刻 PHP 的 (float)"12.5abc" == 12.5。
func floatPrefix(s string) float64 {
	m := floatPrefixRe.FindString(strings.TrimSpace(s))
	if m == "" {
		return 0
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return 0
	}
	return f
}

// phpStr 复刻 PHP 的 (string) 强转（原生 action 里只用于 token 之类的字符串参数）。
func phpStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "1"
		}
		return ""
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	}
	return ""
}
