package phpfilter

import (
	"strings"
	"testing"
)

// TestValidateEmail 覆盖 PHP FILTER_VALIDATE_EMAIL 的关键分支。
// 期望值来自 php-src ext/filter/logical_filters.c 里 regexp1 的结构：
// 域名必须至少两段、末尾标签必须以字母（或 xn--）开头、标签不能以 '-' 开头/结尾。
func TestValidateEmail(t *testing.T) {
	valid := []string{
		"a@b.co",
		"user.name+tag@example.com",
		"12345678@qq.com",
		"a_b-c@sub.domain.org",
		"a@b.c", // TLD 单个字母在 PHP 里是合法的
		"a@b.c0m",
		"a@xn--fiqs8s.example",
		`"quoted"@example.com`,
		`"a.b"@example.com`,
		"a@[1.2.3.4]",
		"a@[IPv6:2001:db8::1]",
	}
	for _, e := range valid {
		if !ValidateEmail(e) {
			t.Errorf("应判为合法: %q", e)
		}
	}
	invalid := []string{
		"", "a", "a@", "@b.co", "a@b", "a@b.", "a@.b", "a@b..c",
		"a b@c.d", "a@b_c.com", "a..b@c.d", ".a@c.d", "a.@c.d",
		"a@b.c-", "a@-b.c", "a@b.-c", "用户@qq.com", "a@1.2.3.4",
		"a@b.c,d", "a@b/c.d", `a"b@c.d`, `"quoted local"@example.com`,
	}
	for _, e := range invalid {
		if ValidateEmail(e) {
			t.Errorf("应判为非法: %q", e)
		}
	}
	// 本地部分 >= 65 字节 -> 拒绝（原正则的第二条否定前瞻）。
	long := strings.Repeat("a", 65) + "@example.com"
	if ValidateEmail(long) {
		t.Errorf("本地部分 65 字节应判非法")
	}
	if !ValidateEmail(strings.Repeat("a", 64) + "@example.com") {
		t.Errorf("本地部分 64 字节应判合法")
	}
	// 整体 >= 255 字节 -> 拒绝（第一条否定前瞻）。
	if ValidateEmail(strings.Repeat("a", 240) + "@example.com") {
		t.Errorf("整体 253 字节以上应判非法")
	}
	// 域名标签 >= 64 字节 -> 拒绝。
	if ValidateEmail("a@" + strings.Repeat("b", 64) + ".com") {
		t.Errorf("标签 64 字节应判非法")
	}
}
