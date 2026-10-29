package uc

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 本文件是 uc.php 里依赖的那几处 PHP 语义的 Go 复刻。
// 逐条对应，改动前先回看 uc.php 的对应行。

// phpEmpty 复刻 empty()：null / false / 0 / 0.0 / "" / "0" / 空数组都算 empty。
func phpEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == "" || t == "0"
	case float64:
		return t == 0
	case int64:
		return t == 0
	case int:
		return t == 0
	case json.Number:
		if t.String() == "0" {
			return true
		}
		if f, err := t.Float64(); err == nil {
			return f == 0
		}
		return false
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// phpStr 复刻字符串上下文里的取值（PHP 会把 int/float 隐式转字符串）。
func phpStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "1"
		}
		return ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// phpInt 复刻 (int) 强制转换：float 向零截断，字符串取前导数字部分（PHP 8 对
// 纯非数字串返回 0 并告警，这里同为 0）。
func phpInt(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if f, err := t.Float64(); err == nil {
			return int64(f)
		}
		return 0
	case string:
		return phpLeadingInt(t)
	default:
		return 0
	}
}

// phpLeadingInt 复刻 (int)"123abc" == 123：跳过前导空白，取可选符号 + 连续数字。
func phpLeadingInt(s string) int64 {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	i := 0
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0
	}
	n, err := strconv.ParseInt(s[start:i], 10, 64)
	if err != nil {
		return 0
	}
	if neg {
		return -n
	}
	return n
}

// phpRound2 复刻 round($x, 2)（PHP 默认 PHP_ROUND_HALF_UP，正数即四舍五入远离零）。
func phpRound2(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	// 先按 1e2 放大再用 half-away-from-zero，与 PHP round() 的十进制半进一致。
	if x >= 0 {
		return math.Floor(x*100+0.5) / 100
	}
	return math.Ceil(x*100-0.5) / 100
}

// pathinfoExt 复刻 pathinfo($name, PATHINFO_EXTENSION)（Linux 语义：只按 '/' 分目录）。
// 无 '.' 或以 '.' 结尾 → ""；".gitignore" → "gitignore"（与 PHP 一致）。
func pathinfoExt(name string) string {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	i := strings.LastIndexByte(base, '.')
	if i < 0 || i == len(base)-1 {
		return ""
	}
	return base[i+1:]
}

// phpURLEncode 复刻 urlencode()（http_build_query 的 RFC1738 编码）：
// 仅保留 A-Za-z0-9-_.，空格变 '+'，其余 %XX 大写。
func phpURLEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// phpRawURLEncode 复刻 rawurlencode()（RFC3986）：额外保留 '~'，空格变 %20。
func phpRawURLEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// phpBuildQuery 复刻 http_build_query()：**按传入顺序**拼接（Go 的 url.Values.Encode
// 会按键排序，UC 服务端对参数序敏感，所以必须自己拼）。
func phpBuildQuery(pairs [][2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, phpURLEncode(p[0])+"="+phpURLEncode(p[1]))
	}
	return strings.Join(parts, "&")
}

// jsonQuote 复刻 json_encode 对单个字符串的输出（含引号）。
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
