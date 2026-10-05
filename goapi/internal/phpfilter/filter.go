// Package phpfilter 复刻 PHP ext/filter 里被本仓库用到的判定函数。
//
// 目前只有 FILTER_VALIDATE_EMAIL：它被 api.php 的 send_code / register /
// email_verify_send / email_verify / admin 等 action 使用，Go 侧必须逐字对齐
// 「哪些邮箱算合法」，否则客户端会遇到「以前能注册的邮箱现在报格式不正确」。
package phpfilter

import (
	"net"
	"regexp"
	"strings"
)

// 下面四个正则来自 php-src ext/filter/logical_filters.c 的 php_filter_validate_email
// （regexp1，即不带 FILTER_FLAG_EMAIL_UNICODE 的那条），逐段拆开后用 Go 的 RE2 表达。
// 原正则含否定前瞻（(?!(?:…){255,}) 等）与 /D 标志，RE2 不支持前瞻，
// 所以长度与整体结构那两处在 ValidateEmail 里用代码实现，语义等价。
var (
	// 本地部分的「原子」片段（不含点，点只做分隔符）。
	emailLocalSegRe = regexp.MustCompile(`^[\x21\x23-\x27\x2A\x2B\x2D-\x39\x3D\x3F\x5E-\x7E]+$`)
	// 本地部分的引号字符串。
	emailQuotedRe = regexp.MustCompile(`^"(?:[\x01-\x08\x0B\x0C\x0E-\x1F\x21\x23-\x5B\x5D-\x7F]|\\[\x00-\x7F])*"$`)
	// 域名中间标签：(?:xn--)?[a-z0-9]+(?:-+[a-z0-9]+)*
	emailDomainLabelRe = regexp.MustCompile(`^(?i:(?:xn--)?[a-z0-9]+(?:-+[a-z0-9]+)*)$`)
	// 域名末尾标签（TLD）：(?:[a-z][a-z0-9]*|xn--[a-z0-9]+)(?:-+[a-z0-9]+)*
	emailDomainTLDRe = regexp.MustCompile(`^(?i:(?:(?:[a-z][a-z0-9]*)|(?:xn--[a-z0-9]+))(?:-+[a-z0-9]+)*)$`)
)

// ValidateEmail 复刻 filter_var($e, FILTER_VALIDATE_EMAIL)（非 UNICODE 分支）。
//
// 结构（对应原正则从左到右）：
//
//	^(?!(?:…){255,})            —— 整串能被 255 个「单元」覆盖则拒绝（≈ 字节数 >= 255）
//	 (?!(?:…){65,}@)            —— 本地部分能被 65 个单元覆盖则拒绝（≈ 字节数 >= 65）
//	 (?:atom|"quoted")(?:\.(?:atom|"quoted"))*@
//	 (?:host|\[ipliteral\])$/iD
func ValidateEmail(s string) bool {
	// 第一道闸：RFC 2821 的 320 字节上限。
	if len(s) > 320 {
		return false
	}
	// 任一单元至少消费 1 字节 => 255 个单元要求 >= 255 字节。
	if len(s) >= 255 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 {
		return false
	}
	// 本地部分与域名都不允许出现 @，所以必须只有一个。
	if strings.IndexByte(s[at+1:], '@') >= 0 {
		return false
	}
	local, domain := s[:at], s[at+1:]
	if domain == "" {
		return false
	}
	if len(local) >= 65 {
		return false
	}
	if !localOK(local) {
		return false
	}
	return domainOK(domain)
}

// localOK 复刻 (?:atom|"quoted")(?:\.(?:atom|"quoted"))*。
func localOK(local string) bool {
	i := 0
	for {
		if i < len(local) && local[i] == '"' {
			// 找未转义的收尾引号。
			j := i + 1
			for j < len(local) {
				if local[j] == '\\' {
					j += 2
					continue
				}
				if local[j] == '"' {
					break
				}
				j++
			}
			if j >= len(local) {
				return false
			}
			j++ // 收尾引号也算在片段里
			if !emailQuotedRe.MatchString(local[i:j]) {
				return false
			}
			i = j
		} else {
			j := i
			for j < len(local) && local[j] != '.' {
				j++
			}
			if j == i || !emailLocalSegRe.MatchString(local[i:j]) {
				return false
			}
			i = j
		}
		if i == len(local) {
			return true
		}
		if local[i] != '.' {
			return false
		}
		i++ // 吃掉分隔点
		if i == len(local) {
			return false // 尾随点
		}
	}
}

// domainOK 复刻 (?!.*[^.]{64,})(?:(?:label\.){1,126}){1,}(?:tld)(?:-+[a-z0-9]+)*|\[ip\]。
func domainOK(domain string) bool {
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		inner := domain[1 : len(domain)-1]
		if len(inner) > 5 && strings.EqualFold(inner[:5], "ipv6:") {
			inner = inner[5:]
		}
		// Go 的 net.ParseIP 与 PHP 正则都接受点分 IPv4 与各形态 IPv6，
		// 且都拒绝前导零（Go 1.17 起）。
		return net.ParseIP(inner) != nil
	}
	// (?!.*[^.]{64,})：任何不含点的连续段达到 64 字节即拒绝。
	run := 0
	for i := 0; i < len(domain); i++ {
		if domain[i] == '.' {
			run = 0
			continue
		}
		run++
		if run >= 64 {
			return false
		}
	}
	labels := strings.Split(domain, ".")
	// 必须至少有「中间标签 + 末尾标签」两段 => 至少一个点；上限 127 段。
	if len(labels) < 2 || len(labels) > 127 {
		return false
	}
	for i, l := range labels {
		if l == "" {
			return false // 前导/尾随/连续点
		}
		if i == len(labels)-1 {
			if !emailDomainTLDRe.MatchString(l) {
				return false
			}
		} else if !emailDomainLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}
