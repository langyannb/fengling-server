package uc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Auth 对齐 uc_auth.php 里 var_export 出来的数组键（uc.php:20-57, 213-218, 270-276）：
//
//	'cookie'   => string
//	'ts'       => int      （登录时间戳；只有 qrPoll / loginByCookie 写）
//	'nickname' => string
//	'account'  => string
//	'source'   => string   （仅 loginByCookie 写 'manual'）
type Auth struct {
	Cookie   string
	TS       int64
	Nickname string
	Account  string
	Source   string
}

// Status 对齐 UcDrive::status()（uc.php:46-57）：
// 未登录返回 ['logged_in'=>false]，登录返回五个键。api.php 直接 json 输出它。
func Status() map[string]any {
	a := LoadAuth()
	// PHP 是 empty($a['cookie'])："" 与 "0" 都算未登录。
	if a.Cookie == "" || a.Cookie == "0" {
		return map[string]any{"logged_in": false}
	}
	return map[string]any{
		"logged_in":  true,
		"login_time": a.TS,
		"nickname":   a.Nickname,
		"account":    a.Account,
		"cookie_len": len(a.Cookie),
	}
}

// LoadAuth 对齐 UcDrive::loadAuth()（uc.php:27-33）：文件不存在 / 解析不出数组 → 空。
func LoadAuth() Auth {
	b, err := os.ReadFile(AuthPath)
	if err != nil {
		return Auth{}
	}
	m := parseAuthPHP(string(b))
	a := Auth{
		Cookie:   phpStr(m["cookie"]),
		Nickname: phpStr(m["nickname"]),
		Account:  phpStr(m["account"]),
		Source:   phpStr(m["source"]),
	}
	if v, ok := m["ts"]; ok {
		a.TS = phpInt(v)
	}
	return a
}

// SaveAuth 对齐 UcDrive::saveAuth()（uc.php:35-39），写出 php 侧可直接 include 的文本：
//
//	<?php
//	// UC 网盘登录凭据（扫码登录自动生成，请勿手动编辑）
//	return array (
//	  'cookie' => '...',
//	  ...
//	);
func SaveAuth(a Auth) bool {
	var b strings.Builder
	b.WriteString("<?php\n")
	b.WriteString("// UC 网盘登录凭据（扫码登录自动生成，请勿手动编辑）\n")
	b.WriteString("return array (\n")
	fmt.Fprintf(&b, "  'cookie' => %s,\n", phpVarExportString(a.Cookie))
	fmt.Fprintf(&b, "  'ts' => %d,\n", a.TS)
	fmt.Fprintf(&b, "  'nickname' => %s,\n", phpVarExportString(a.Nickname))
	fmt.Fprintf(&b, "  'account' => %s,\n", phpVarExportString(a.Account))
	if a.Source != "" {
		fmt.Fprintf(&b, "  'source' => %s,\n", phpVarExportString(a.Source))
	}
	b.WriteString(");\n")

	if dir := filepath.Dir(AuthPath); dir != "" && dir != "." {
		// PHP 侧 docroot 一定存在，这里只是让 Go 侧的路径配置更宽容。
		_ = os.MkdirAll(dir, 0o755)
	}
	// 先写临时文件再 rename：避免 PHP 与 Go 同时读到半截文件。
	// 权限与 PHP file_put_contents 默认（0666 & ~umask）一致，取 0644。
	tmp := AuthPath + ".gotmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return false
	}
	if err := os.Rename(tmp, AuthPath); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	return true
}

// ClearAuth 对齐 UcDrive::clearAuth()（uc.php:41-44）：@unlink，失败忽略。
func ClearAuth() {
	_ = os.Remove(AuthPath)
}

// phpVarExportString 复刻 var_export() 的字符串输出：单引号包裹，只转义 \ 与 '。
func phpVarExportString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

var (
	authPropRe = regexp.MustCompile(`^\s*'([^']+)'\s*=>\s*(.+?)\s*,?\s*$`)
	authIntRe  = regexp.MustCompile(`^-?\d+$`)
)

// parseAuthPHP 从 var_export(array) 写出的 PHP 文件里取顶层标量键。
// 与 httpapi 侧 parseUCAuthPHP 同源（那边处理 status/logout，这里需要全字段）。
// 只认单行标量：凭据数组就是扁平标量表，没有嵌套。
func parseAuthPHP(src string) map[string]any {
	out := map[string]any{}
	for _, line := range strings.Split(src, "\n") {
		m := authPropRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		raw := strings.TrimSpace(m[2])
		switch {
		case len(raw) >= 2 && strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'"):
			out[key] = phpUnescapeSingleQuoted(raw[1 : len(raw)-1])
		case authIntRe.MatchString(raw):
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				out[key] = n
			}
		case raw == "true":
			out[key] = true
		case raw == "false":
			out[key] = false
		}
	}
	return out
}

// phpUnescapeSingleQuoted 复刻 PHP 单引号字符串：只有 \\ 与 \' 是转义。
func phpUnescapeSingleQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '\'') {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
