package httpapi

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"

	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// statusForCode 逐字对齐 config.php:50-59 的 HTTP 状态码映射。
//
// 特别注意：403（如 require_admin 的「没有权限」）走 else 分支 => HTTP **400**，
// 但 JSON 体里的 code 仍是 403。契约文档里那句「401/403 都是 HTTP 400」是错的，
// 以源码为准：401→401、404→404、500→500、其余非 0→400。
func statusForCode(code int) int {
	switch code {
	case 0:
		return http.StatusOK
	case 401:
		return http.StatusUnauthorized
	case 404:
		return http.StatusNotFound
	case 500:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

// JSON 逐字对齐 config.php:47 json_out($data, $code, $msg, $cacheSeconds)。
//
// 输出壳固定为 `{"code":…,"msg":…,"data":…}`（键序 = PHP 数组插入顺序），
// 编码规则走 phpjson（中文不转义、`/` 转义成 `\/`、紧凑无空格）。
func (c *Ctx) JSON(data any, code int, msg string, cacheSeconds int) {
	if msg == "" {
		msg = "ok"
	}
	h := c.W.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if cacheSeconds > 0 {
		h.Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheSeconds))
	} else {
		h.Set("Cache-Control", "no-store")
	}
	body := phpjson.Marshal(phpjson.New().
		Set("code", code).
		Set("msg", msg).
		Set("data", data))
	c.writeShell(statusForCode(code), body)
}

// Error 对齐 config.php:76 json_error($msg, $code)：data 恒为 null。
func (c *Ctx) Error(msg string, code int) {
	c.JSON(nil, code, msg, 0)
}

func (c *Ctx) writeShell(status int, body []byte) {
	if acceptsGzip(c.R) {
		if gz, err := gzipPHP(body); err == nil {
			c.W.Header().Set("Content-Encoding", "gzip")
			body = gz
		}
	}
	c.W.WriteHeader(status)
	_, _ = c.W.Write(body)
}

// acceptsGzip 对齐 config.php:67 的 strpos($_SERVER['HTTP_ACCEPT_ENCODING'], 'gzip') !== false
// —— 大小写敏感的**子串**判断，不是解析 Accept-Encoding。
func acceptsGzip(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// gzipPHP 复刻 PHP gzencode 的默认参数：zlib 默认压缩级别（=6）、mtime=0、OS 字节=3(Unix)。
// deflate 流不保证与 zlib 逐字节相同（解压后一定相同），gzip 头已对齐。
func gzipPHP(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, 6)
	if err != nil {
		return nil, err
	}
	zw.Header.OS = 3 // PHP gzencode 写的是 Unix
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// setCORS 逐字对齐 api.php:27-30。
// 只在原生 action 与 OPTIONS 上设置：透传路径由 PHP 自己发这三个头，
// 这里再发一次会让响应出现重复的 Access-Control-Allow-Origin。
func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}
