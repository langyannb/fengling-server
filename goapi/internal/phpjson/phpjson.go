// Package phpjson 复刻 PHP json_encode($v, JSON_UNESCAPED_UNICODE) 的**字节级**输出。
//
// 为什么不用 encoding/json（客户端零改动是最高优先级，原生 action 必须逐字节对齐 PHP）：
//  1. PHP 默认把 `/` 转义成 `\/`（除非显式传 JSON_UNESCAPED_SLASHES，api.php/data 侧没有传）；
//     encoding/json 永远输出裸 `/`。
//  2. Go 的 map 遍历顺序随机，而 PHP 是「数组插入顺序」；这里用有序对象 O 显式定序。
//  3. PHP 的浮点是 serialize_precision=-1 语义：整数浮点输出 `12.0` 而不是 `12`
//     （json_out 里 size_mb 走 (float) 强转，所以这一点是真实差异）。
//  4. encoding/json 默认把 `<` `>` `&` 转成 `\u003c` 之类，PHP 不转（这里 SetEscapeHTML(false)）。
package phpjson

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// O 是有序 JSON 对象：Set 的调用顺序就是输出顺序（对应 PHP 数组的插入顺序）。
type O struct {
	keys []string
	vals []any
}

// New 建一个空的有序对象。
func New() *O { return &O{} }

// Set 追加一个键值对（已存在的键不会被覆盖，与 PHP 顺序数组的追加语义一致）。
func (o *O) Set(k string, v any) *O {
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
	return o
}

// Len 返回键个数。
func (o *O) Len() int { return len(o.keys) }

// Get 取一个键的值。
func (o *O) Get(k string) (any, bool) {
	for i, key := range o.keys {
		if key == k {
			return o.vals[i], true
		}
	}
	return nil, false
}

// Put 是 PHP 的 `$arr[$k] = $v` 语义：键已存在则**原位改值**（输出顺序不变），
// 否则追加到末尾。Set 只做追加，所以「先构造全量默认值、再逐字段覆盖」的场景必须用 Put
// （抽奖的 lottery_window_state / lottery_config 都是这种写法）。
func (o *O) Put(k string, v any) *O {
	for i, key := range o.keys {
		if key == k {
			o.vals[i] = v
			return o
		}
	}
	return o.Set(k, v)
}

// Keys 返回键的插入顺序（只读快照，调用方不得修改底层切片）。
func (o *O) Keys() []string { return o.keys }

// Marshal 等价于 PHP 的 json_encode($v, JSON_UNESCAPED_UNICODE)（紧凑、无空格换行）。
func Marshal(v any) []byte {
	return appendValue(make([]byte, 0, 256), v)
}

func appendValue(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case *O:
		b = append(b, '{')
		for i := range x.keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = AppendString(b, x.keys[i])
			b = append(b, ':')
			b = appendValue(b, x.vals[i])
		}
		return append(b, '}')
	case bool:
		if x {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case string:
		return AppendString(b, x)
	case []byte:
		return AppendString(b, string(x))
	case int:
		return strconv.AppendInt(b, int64(x), 10)
	case int8:
		return strconv.AppendInt(b, int64(x), 10)
	case int16:
		return strconv.AppendInt(b, int64(x), 10)
	case int32:
		return strconv.AppendInt(b, int64(x), 10)
	case int64:
		return strconv.AppendInt(b, x, 10)
	case uint:
		return strconv.AppendUint(b, uint64(x), 10)
	case uint8:
		return strconv.AppendUint(b, uint64(x), 10)
	case uint16:
		return strconv.AppendUint(b, uint64(x), 10)
	case uint32:
		return strconv.AppendUint(b, uint64(x), 10)
	case uint64:
		return strconv.AppendUint(b, x, 10)
	case float32:
		return AppendFloat(b, float64(x))
	case float64:
		return AppendFloat(b, x)
	case json.Number:
		return appendNumber(b, x)
	case []any:
		b = append(b, '[')
		for i, e := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendValue(b, e)
		}
		return append(b, ']')
	case []string:
		b = append(b, '[')
		for i, e := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = AppendString(b, e)
		}
		return append(b, ']')
	default:
		return appendFallback(b, v)
	}
}

// appendNumber 处理 json.Number（数据库里取出来的 JSON 字面量，保留 int/float 形态）。
func appendNumber(b []byte, n json.Number) []byte {
	s := n.String()
	if s == "" {
		return append(b, '0')
	}
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return append(b, s...)
		}
		return AppendFloat(b, f)
	}
	return append(b, s...)
}

const hexLower = "0123456789abcdef"

// AppendString 按 PHP 的转义规则输出 JSON 字符串（不转义非 ASCII，转义 `/`）。
func AppendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '/':
			b = append(b, '\\', '/')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', hexLower[c>>4], hexLower[c&0xF])
			} else {
				b = append(b, c)
			}
		}
	}
	return append(b, '"')
}

// AppendFloat 按 PHP json_encode 的规则输出 float64（整数浮点带 `.0`，指数形式小写 e）。
func AppendFloat(b []byte, f float64) []byte {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		// PHP 的 json_encode 对 NAN/INF 会整体失败（返回 false）；这里退化为 0，绝不 panic。
		return append(b, '0')
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	mant, exp := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, exp = s[:i], s[i+1:]
	}
	if !strings.Contains(mant, ".") {
		mant += ".0"
	}
	if exp == "" {
		return append(b, mant...)
	}
	// PHP: 1.0e+25 / 1.0e-5（符号保留、不补前导零）
	sign := byte('+')
	if strings.HasPrefix(exp, "+") {
		exp = exp[1:]
	} else if strings.HasPrefix(exp, "-") {
		sign = '-'
		exp = exp[1:]
	}
	exp = strings.TrimLeft(exp, "0")
	if exp == "" {
		exp = "0"
	}
	b = append(b, mant...)
	b = append(b, 'e')
	b = append(b, sign)
	return append(b, exp...)
}

// appendFallback 只用于本仓库不会走到的类型（map / 结构体等）：用 encoding/json 兜底，
// 再按 PHP 规则补 `/` 的转义。语义上不保证逐字节一致，仅避免 panic。
func appendFallback(b []byte, v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return append(b, "null"...)
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	for _, c := range raw {
		if c == '/' {
			b = append(b, '\\')
		}
		b = append(b, c)
	}
	return b
}
