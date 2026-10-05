package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpfilter"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 这个文件集中放「PHP 语义复刻」的小工具：阶段 2 的账号 action 必须与 PHP 逐字一致，
// 所以不能直接用 Go 的 strings/net 等「更现代但不一样」的行为。

// ---------- 字符串 ----------

// phpTrim 复刻 PHP trim() 的默认字符集 " \t\n\r\x00\x0B"。
// 刻意不用 strings.TrimSpace：它会额外吃掉全角空格等 Unicode 空白，PHP 不会。
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\x0B") }

// phpFalsy 复刻 PHP 的 if (!$s)：空串与字符串 "0" 为假（"0.0"/"00" 是真）。
func phpFalsy(s string) bool { return s == "" || s == "0" }

// mbStrlen 复刻 mb_strlen($s)（UTF-8 字符数，不是字节数）。
func mbStrlen(s string) int { return utf8.RuneCountInString(s) }

// asciiUpper 复刻 strtoupper()：只对 ASCII a-z 生效（不碰 Unicode）。
func asciiUpper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

// randomHex 复刻 bin2hex(random_bytes(n))：2n 位小写十六进制。
// 与 PHP 一样用 CSPRNG；rand.Read 失败在现代内核上不会发生，失败时返回空串
// （调用方会把空 token 当作未登录，绝不会退化成可预测的 token）。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// phpStrtotime 复刻 strtotime() 对 MySQL DATETIME 文本的解析（按 Asia/Shanghai）。
//
// 为什么固定时区：api.php:25 有 date_default_timezone_set('Asia/Shanghai')，
// 而库里的 NOW() 时间戳就是按服务器时区写的；Go 进程的 TZ 未必一致，
// 所以必须显式按 config.LocalZone() 解释，不能用 time.Local。
func phpStrtotime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"2006/01/02 15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, config.LocalZone()); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// nowUnix 就是 PHP 的 time()（绝对时间，与时区无关）。
func nowUnix() int64 { return time.Now().Unix() }

// clientIP 对齐 config.php:82 client_ip()：X-Forwarded-For 整串优先，否则 REMOTE_ADDR。
func clientIP(r *http.Request) string {
	// 用 map 取值而不是 Header.Get：PHP 的 ?? 只判「有没有这个键」，
	// 空头（X-Forwarded-For:）在 PHP 里也算「有」，此时不再回落 REMOTE_ADDR。
	if vs, ok := r.Header[http.CanonicalHeaderKey("X-Forwarded-For")]; ok && len(vs) > 0 {
		return vs[0]
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// ---------- 行取值（store.Row → PHP 标量语义） ----------

// rowStr 取字符串（缺列/为 NULL 时返回 def），对应 PHP 的 `$row['k'] ?? def` 再 (string)。
func rowStr(r store.Row, key, def string) string {
	v, ok := r.Get(key)
	if !ok || v == nil {
		return def
	}
	return phpStr(v)
}

// rowInt 取整数，对应 PHP 的 `(int)($row['k'] ?? def)`。
func rowInt(r store.Row, key string, def int64) int64 {
	v, ok := r.Get(key)
	if !ok || v == nil {
		return def
	}
	return phpInt(v)
}

// rowRaw 对应 PHP 的 `$row['k']`（无 ??）：缺列或 NULL 时**输出 null**（不是空串）。
// user_public 里的 username 就是这个语义。
func rowRaw(r store.Row, key string) any {
	v, ok := r.Get(key)
	if !ok || v == nil {
		return nil
	}
	return phpStr(v)
}

// ---------- 用户公开结构 ----------

// userPublic 对齐 api.php:3377 user_public()，字段顺序 = JSON 输出顺序：
// id username nickname email email_verified avatar bio role is_active created_at tags。
// 绝不含 password / token。
func userPublic(u store.Row) *phpjson.O {
	return phpjson.New().
		Set("id", rowInt(u, "id", 0)).
		Set("username", rowRaw(u, "username")).
		Set("nickname", rowStr(u, "nickname", "")).
		Set("email", rowStr(u, "email", "")).
		Set("email_verified", rowInt(u, "email_verified", 0)).
		Set("avatar", rowStr(u, "avatar", "")).
		Set("bio", rowStr(u, "bio", "")).
		Set("role", rowStr(u, "role", "user")).
		Set("is_active", rowInt(u, "is_active", 1)).
		Set("created_at", rowStr(u, "created_at", "")).
		Set("tags", userTagsArr(u))
}

// userTagsArr 对齐 api.php:3833 user_tags_arr()：逗号分隔、逐项 trim、
// 空串或超过 10 个字跳过、严格去重、最多 5 个。
func userTagsArr(u store.Row) []string {
	raw := phpTrim(rowStr(u, "tags", ""))
	out := []string{}
	if raw == "" {
		return out
	}
	for _, t := range strings.Split(raw, ",") {
		t = phpTrim(t)
		if t == "" || mbStrlen(t) > 10 {
			continue
		}
		dup := false
		for _, x := range out {
			if x == t {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, t)
		}
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// qqEmailRe 对齐 api.php:3403 `/^(\d{5,12})@(qq\.com|vip\.qq\.com)$/i`。
var qqEmailRe = regexp.MustCompile(`(?i)^(\d{5,12})@(qq\.com|vip\.qq\.com)$`)

// qqAvatarFromEmail 对齐 api.php:3400：QQ 邮箱给一个默认头像，其它返回空串。
func qqAvatarFromEmail(email string) string {
	m := qqEmailRe.FindStringSubmatch(phpTrim(email))
	if m == nil {
		return ""
	}
	return "https://q1.qlogo.cn/g?b=qq&nk=" + m[1] + "&s=640"
}

// phpValidateEmail 复刻 filter_var($e, FILTER_VALIDATE_EMAIL)（实现在 internal/phpfilter）。
func phpValidateEmail(s string) bool { return phpfilter.ValidateEmail(s) }
