package uc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempAuthPath 把 AuthPath 指到临时目录，用例结束还原。
func withTempAuthPath(t *testing.T) string {
	t.Helper()
	old := AuthPath
	AuthPath = filepath.Join(t.TempDir(), "uc_auth.php")
	t.Cleanup(func() { AuthPath = old })
	return AuthPath
}

func TestAuthSaveLoadRoundTrip(t *testing.T) {
	path := withTempAuthPath(t)
	in := Auth{
		Cookie:   `ctoken=abc; __puus=x'y\z; a=1`,
		TS:       1759000000,
		Nickname: "小'明\\",
		Account:  "138****8888",
		Source:   "manual",
	}
	if !SaveAuth(in) {
		t.Fatalf("SaveAuth 失败")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("凭据文件应存在: %v", err)
	}
	got := LoadAuth()
	if got != in {
		t.Fatalf("往返不一致:\n got %#v\nwant %#v", got, in)
	}
}

func TestAuthFileIsPHPArrayText(t *testing.T) {
	path := withTempAuthPath(t)
	if !SaveAuth(Auth{Cookie: "ctoken=1", TS: 1759000000, Nickname: "昵称", Account: "acc"}) {
		t.Fatalf("SaveAuth 失败")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件: %v", err)
	}
	src := string(b)
	// 头部注释与 return array (...) 结构必须能被 PHP include。
	if !strings.HasPrefix(src, "<?php\n// UC 网盘登录凭据（扫码登录自动生成，请勿手动编辑）\nreturn array (\n") {
		t.Fatalf("文件头不符:\n%s", src)
	}
	if !strings.HasSuffix(src, ");\n") {
		t.Fatalf("文件尾应为 \");\\n\"，得到 %q", src[len(src)-8:])
	}
	for _, want := range []string{
		"  'cookie' => 'ctoken=1',\n",
		"  'ts' => 1759000000,\n",
		"  'nickname' => '昵称',\n",
		"  'account' => 'acc',\n",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("缺少行 %q\n全文:\n%s", want, src)
		}
	}
	// ts 必须是整数（不能是字符串 '1759000000'），否则 PHP 侧 status() 的类型会变。
	if strings.Contains(src, "'1759000000'") {
		t.Fatalf("ts 不应加引号:\n%s", src)
	}
	// source 为空时不该出现该行（对齐 qrPoll 保存的四个键）。
	if strings.Contains(src, "'source'") {
		t.Fatalf("未设置 Source 时不应写 source 行:\n%s", src)
	}
}

func TestAuthSingleQuoteEscaping(t *testing.T) {
	path := withTempAuthPath(t)
	SaveAuth(Auth{Cookie: `a\b'c`, Nickname: "x", Account: "y"})
	b, _ := os.ReadFile(path)
	src := string(b)
	// var_export：\ → \\，' → \'
	if !strings.Contains(src, `'cookie' => 'a\\b\'c',`) {
		t.Fatalf("单引号转义不符:\n%s", src)
	}
	if got := LoadAuth().Cookie; got != `a\b'c` {
		t.Fatalf("反转义不一致: %q", got)
	}
}

func TestAuthLoadParsesPHPSource(t *testing.T) {
	// 手工构造一段 PHP var_export 输出（含 \r\n 之外的常见形态），验证解析器。
	path := withTempAuthPath(t)
	src := "<?php\n// UC 网盘登录凭据（扫码登录自动生成，请勿手动编辑）\n" +
		"return array (\n" +
		"  'cookie' => 'ctoken=zz; __puus=p',\n" +
		"  'ts' => 1759123456,\n" +
		"  'nickname' => '老\\'王',\n" +
		"  'account' => '139****0000',\n" +
		"  'source' => 'manual',\n" +
		");\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("写样本: %v", err)
	}
	got := LoadAuth()
	mustEqual(t, "cookie", got.Cookie, "ctoken=zz; __puus=p")
	mustEqual(t, "ts", got.TS, int64(1759123456))
	mustEqual(t, "nickname", got.Nickname, "老'王")
	mustEqual(t, "account", got.Account, "139****0000")
	mustEqual(t, "source", got.Source, "manual")
}

func TestAuthLoadMissingFile(t *testing.T) {
	withTempAuthPath(t)
	if got := LoadAuth(); got != (Auth{}) {
		t.Fatalf("文件不存在应返回零值，得到 %#v", got)
	}
}

func TestAuthClear(t *testing.T) {
	path := withTempAuthPath(t)
	SaveAuth(Auth{Cookie: "x"})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件应存在: %v", err)
	}
	ClearAuth()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ClearAuth 应删除文件: %v", err)
	}
	// 重复调用不该 panic（PHP 是 @unlink）。
	ClearAuth()
}

func TestStatusLoggedOut(t *testing.T) {
	withTempAuthPath(t)
	got := Status()
	mustValue(t, "logged_in", got["logged_in"], false)
	if len(got) != 1 {
		t.Fatalf("未登录只应有 logged_in，得到 %#v", got)
	}
	// cookie 为 "0" 时 PHP empty() 也算未登录。
	SaveAuth(Auth{Cookie: "0", TS: 1})
	mustValue(t, "cookie=0 视为未登录", Status()["logged_in"], false)
}

func TestStatusLoggedIn(t *testing.T) {
	withTempAuthPath(t)
	SaveAuth(Auth{Cookie: "ctoken=abc", TS: 1759000000, Nickname: "小明", Account: "138****8888"})
	got := Status()
	mustValue(t, "logged_in", got["logged_in"], true)
	mustValue(t, "login_time", got["login_time"], int64(1759000000))
	mustValue(t, "nickname", got["nickname"], "小明")
	mustValue(t, "account", got["account"], "138****8888")
	mustValue(t, "cookie_len", got["cookie_len"], len("ctoken=abc"))
	if len(got) != 5 {
		t.Fatalf("登录态应有五个键，得到 %#v", got)
	}
}
