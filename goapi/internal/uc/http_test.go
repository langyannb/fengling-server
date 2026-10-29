package uc

import (
	"regexp"
	"strings"
	"testing"
)

func TestTSMillis(t *testing.T) {
	got := ts()
	if !regexp.MustCompile(`^\d{13}$`).MatchString(got) {
		t.Fatalf("ts() 应为 13 位毫秒字符串，得到 %q", got)
	}
}

func TestCToken(t *testing.T) {
	cases := []struct{ cookie, want string }{
		{"ctoken=abc123; __puus=zz", "abc123"},
		{"__puus=zz; ctoken=abc123", "abc123"},
		{"a=1;  ctoken=x-y_z.9; b=2", "x-y_z.9"},
		{"nctoken=no; ctoken=", ""},
		{"aaa=1", ""},
		{"", ""},
		// 与 PHP 正则 (?:^|;\s*) 一致：ctoken 必须是名首或分号后。
		{"xxctoken=no", ""},
	}
	for _, c := range cases {
		if got := ctoken(c.cookie); got != c.want {
			t.Fatalf("ctoken(%q) = %q, want %q", c.cookie, got, c.want)
		}
	}
}

func TestCookieJarAbsorbAndString(t *testing.T) {
	j := newCookieJar()
	// 多值 Set-Cookie，每个只取第一个分号前；同名覆盖。
	j.absorb([]string{
		"a=1; Path=/; HttpOnly",
		"b=2; Domain=.uc.cn",
		"a=3; Path=/",  // 覆盖 a，但位置仍是第一
		"=bad; Path=/", // 空 name 丢弃
		"noequal",      // 无 '=' 丢弃
		"c=; Path=/",   // 空 value 保留
	})
	if got, want := j.String(), "a=3; b=2; c="; got != want {
		t.Fatalf("jarStr = %q, want %q", got, want)
	}
}

func TestPHPBuildQueryKeepsOrder(t *testing.T) {
	got := phpBuildQuery([][2]string{
		{"client_id", "381"},
		{"v", "1.2"},
		{"request_id", "1759000000000"},
	})
	want := "client_id=381&v=1.2&request_id=1759000000000"
	if got != want {
		t.Fatalf("phpBuildQuery = %q, want %q", got, want)
	}
	// 空值保留 "k="，空格按 RFC1738 变 '+'，中文按 UTF-8 百分号编码。
	got = phpBuildQuery([][2]string{{"passcode", ""}, {"q", "a b"}, {"n", "名"}})
	want = "passcode=&q=a+b&n=%E5%90%8D"
	if got != want {
		t.Fatalf("phpBuildQuery 编码 = %q, want %q", got, want)
	}
}

func TestPHPRawURLEncode(t *testing.T) {
	// PHP rawurlencode 保 '~'，不保 '|' 与 ':'。
	if got, want := phpRawURLEncode("S:custom|C:titlebar_fix"), "S%3Acustom%7CC%3Atitlebar_fix"; got != want {
		t.Fatalf("rawurlencode = %q, want %q", got, want)
	}
	if got, want := phpRawURLEncode("a-_.~ b"), "a-_.~%20b"; got != want {
		t.Fatalf("rawurlencode 空格/波浪 = %q, want %q", got, want)
	}
}

func TestCASHeaders(t *testing.T) {
	got := casHeaders()
	want := []string{
		"accept: application/json, text/plain, */*",
		"content-type: application/x-www-form-urlencoded",
		"origin: https://drive.uc.cn",
		"referer: https://drive.uc.cn/",
		"user-agent: " + UA,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("casHeaders 顺序/内容不一致:\n%q", got)
	}
	if !strings.Contains(UA, "Chrome/146.0.0.0") {
		t.Fatalf("UA 应与 uc.php:15 逐字一致")
	}
}

func TestAPIHeaders(t *testing.T) {
	got := apiHeaders("")
	want := []string{
		"accept: application/json, text/plain, */*",
		"accept-language: zh-CN,zh;q=0.9,en;q=0.8",
		"content-type: application/json;charset=UTF-8",
		"origin: https://drive.uc.cn",
		"referer: https://drive.uc.cn/",
		"sec-fetch-dest: empty",
		"sec-fetch-mode: cors",
		"sec-fetch-site: same-site",
		"user-agent: " + UA,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("apiHeaders(空 cookie) 不一致:\n%q", got)
	}
	// 有 ctoken 才追加 x-ctoken。
	got = apiHeaders("ctoken=zzz; __puus=1")
	if len(got) != len(want)+1 || got[len(got)-1] != "x-ctoken: zzz" {
		t.Fatalf("apiHeaders 应追加 x-ctoken，得到 %q", got)
	}
	// cookie 非空但没有 ctoken：不追加。
	got = apiHeaders("__puus=1")
	if len(got) != len(want) {
		t.Fatalf("无 ctoken 时不应追加 x-ctoken，得到 %q", got)
	}
}

func TestShareKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://drive.uc.cn/s/abc123", "abc123"},
		{"https://drive.uc.cn/s/abc123?pwd=8888", "abc123"},
		{"  https://drive.uc.cn/s/AbC123  ", "AbC123"},
		{"https://drive.uc.cn/x?pwd_id=deadbeef&fid=1", "deadbeef"},
		{"https://drive.uc.cn/x?share_id=feedface", "feedface"},
		{"  AbCd1234Ef  ", "AbCd1234Ef"},
		{"", ""},
		{"abc", ""},                         // 不足 8 位
		{"https://pan.uc.cn/?pwd=1234", ""}, // 无 key
		{"https://drive.uc.cn/s/", ""},      // 空 key
	}
	for _, c := range cases {
		if got := ShareKey(c.in); got != c.want {
			t.Fatalf("ShareKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGuessNameVersion(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		// 常见安卓包名形态
		{"微信_v8.0.42.apk", "微信", "8.0.42"},
		{"QQ_9.1.5.apk", "QQ", "9.1.5"},
		{"抖音 32.5.0 (320500).apk", "抖音 320500", "32.5.0"},
		{"com.example.app-1.2.3-release.apk", "com.example.app", "1.2.3-release"},
		{"淘宝_v10.30.0_x86.apk", "淘宝", "10.30.0_x86"},
		// 无扩展名 / 无版本
		{"无版本", "无版本", ""},
		{"123.apk", "123", ""},
		// 方括号与多余空格
		{"X [1.2] .apk", "X", "1.2"},
		// 只有版本号时回落到 base（PHP 里 name 会被清空后回填 $base）
		{"v2.1-beta.exe", "v2.1-beta", "2.1-beta"},
		// 四个点段上限 3 个
		{"app 1.2.3.4.5.zip", "app .5", "1.2.3.4"},
	}
	for _, c := range cases {
		name, ver := GuessNameVersion(c.in)
		if name != c.name || ver != c.ver {
			t.Fatalf("GuessNameVersion(%q) = (%q,%q), want (%q,%q)", c.in, name, ver, c.name, c.ver)
		}
	}
}

func TestNormFile(t *testing.T) {
	f := map[string]any{
		"file_name":       "Setup.APK",
		"size":            float64(1048576),
		"fid":             "f1",
		"share_fid_token": "ft1",
		"updated_at":      "1759000000",
		"dir":             false,
		"file":            true,
	}
	got := normFile(f)
	mustValue(t, "name", got["name"], "Setup.APK")
	mustValue(t, "size", got["size"], int64(1048576))
	mustValue(t, "size_mb", got["size_mb"], float64(1))
	mustValue(t, "isdir", got["isdir"], false)
	mustValue(t, "fid", got["fid"], "f1")
	mustValue(t, "fid_token", got["fid_token"], "ft1")
	mustValue(t, "ext", got["ext"], "apk")
	mustValue(t, "updated", got["updated"], "1759000000")

	// 无 size 时 size / size_mb 都是 0（PHP 的 isset 分支）。
	got = normFile(map[string]any{"file_name": "noext"})
	mustValue(t, "无 size 的 size", got["size"], int64(0))
	if v, ok := got["size_mb"].(int64); !ok || v != 0 {
		t.Fatalf("无 size 的 size_mb 应为 int 0，得到 %#v", got["size_mb"])
	}
	mustValue(t, "无扩展名", got["ext"], "")
	mustValue(t, "updated 缺省", got["updated"], "")
}

func TestRoundTo2LikePHP(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{100000 / 1048576.0, 0.1},
		{550000 / 1048576.0, 0.52},
		{1572864 / 1048576.0, 1.5},
		{1048576 / 1048576.0, 1},
		{0, 0},
	}
	for _, c := range cases {
		if got := phpRound2(c.in); got != c.want {
			t.Fatalf("phpRound2(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPathinfoExt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a.APK", "APK"},
		{"a.tar.gz", "gz"},
		{"a", ""},
		{"a.", ""},
		{".gitignore", "gitignore"},
		{"dir.d/noext", ""},
		{"dir.d/x.zip", "zip"},
	}
	for _, c := range cases {
		if got := pathinfoExt(c.in); got != c.want {
			t.Fatalf("pathinfoExt(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsDirEntry(t *testing.T) {
	// dir/file 字段优先
	mustEqual(t, "dir=true file=false", isDirEntry(map[string]any{"dir": true, "file": false}), true)
	mustEqual(t, "dir=false file=true", isDirEntry(map[string]any{"dir": false, "file": true}), false)
	mustEqual(t, "只有 file", isDirEntry(map[string]any{"file": true}), false)
	mustEqual(t, "只有空 dir", isDirEntry(map[string]any{"dir": ""}), false)
	// 无 dir/file 时看 file_type
	mustEqual(t, "file_type=1", isDirEntry(map[string]any{"file_type": float64(1)}), true)
	mustEqual(t, "file_type=0", isDirEntry(map[string]any{"file_type": float64(0)}), false)
	mustEqual(t, "无字段", isDirEntry(map[string]any{}), false)
}

func TestPHPHelpers(t *testing.T) {
	if !phpEmpty("0") || phpEmpty("x") || !phpEmpty(nil) || phpEmpty(false) == false {
		t.Fatalf("phpEmpty 语义不对")
	}
	mustEqual(t, "phpInt 字符串前导数字", phpInt("123abc"), int64(123))
	mustEqual(t, "phpInt float 截断", phpInt(float64(2.9)), int64(2))
	mustEqual(t, "phpInt 负数", phpInt("-7x"), int64(-7))
	mustEqual(t, "phpStr nil", phpStr(nil), "")
	mustEqual(t, "phpStr float", phpStr(float64(3)), "3")
	mustEqual(t, "phpStr bool", phpStr(true), "1")
}

func TestDecodeArray(t *testing.T) {
	if _, ok := decodeArray(""); ok {
		t.Fatalf("空 body 不是数组")
	}
	if _, ok := decodeArray("not json"); ok {
		t.Fatalf("非 JSON 不是数组")
	}
	if _, ok := decodeArray("42"); ok {
		t.Fatalf("标量不是数组")
	}
	if m, ok := decodeArray(`{"a":1}`); !ok || phpInt(m["a"]) != 1 {
		t.Fatalf("对象应解析成 map")
	}
	// PHP is_array([]) 也为 true
	if m, ok := decodeArray(`[]`); !ok || len(m) != 0 {
		t.Fatalf("顶层数组应视作空 map")
	}
	// 空 JSON 对象
	if m, ok := decodeArray(`{}`); !ok || len(m) != 0 {
		t.Fatalf("{} 应解析成空 map")
	}
}
