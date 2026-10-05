package httpapi

import (
	"os"
	"strings"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 5A 附带：about_config_get / notice_get 的**读取**（公开、只读）。
// 写入侧 about_config_set / notice_set 继续透传（本单未迁）。

func aboutSettingsFn(key, value string) func(string, []any) (store.Row, error) {
	return func(q string, args []any) (store.Row, error) {
		if strings.Contains(q, "FROM settings WHERE `key` = ?") && phpStr(args[0]) == key {
			return srow("key", key, "value", value), nil
		}
		return nil, nil
	}
}

func TestAboutConfigGetDefaults(t *testing.T) {
	f := newLotteryFake()
	w := callAction(newLotteryRouter(f), "GET", "/api.php?action=about_config_get", "", "", nil)
	want := `{"code":0,"msg":"ok","data":{"qq_group":"","qq_key":"","qq_url":"","qq_channel":"","website":"","github":"","feedback":"","donate":"","banner_text":"风铃分享库 · 官方频道","banner_sub":"最新软件 · 更新通知 · 交流反馈"}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	assertCORS(t, w.Header())
}

func TestAboutConfigGetMergesStoredObject(t *testing.T) {
	f := newLotteryFake()
	// 未知键 extra 必须追加在末尾；已知键原位覆盖；`/` 要被转义成 `\/`（PHP 无 UNESCAPED_SLASHES）。
	f.rowFn = aboutSettingsFn("about_config", `{"website":"https://example.com","extra":"x","banner_text":"自定义"}`)
	w := callAction(newLotteryRouter(f), "GET", "/api.php?action=about_config_get", "", "", nil)
	want := `{"code":0,"msg":"ok","data":{"qq_group":"","qq_key":"","qq_url":"","qq_channel":"","website":"https:\/\/example.com","github":"","feedback":"","donate":"","banner_text":"自定义","banner_sub":"最新软件 · 更新通知 · 交流反馈","extra":"x"}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

func TestAboutConfigGetEmptyValueFallsBackToDefaults(t *testing.T) {
	f := newLotteryFake()
	f.rowFn = aboutSettingsFn("about_config", "")
	w := callAction(newLotteryRouter(f), "GET", "/api.php?action=about_config_get", "", "", nil)
	if !strings.Contains(w.Body.String(), `"banner_text":"风铃分享库 · 官方频道"`) {
		t.Fatalf("空值应当回落默认: %s", w.Body.String())
	}
}

func TestNoticeGetPreservesStoredTypes(t *testing.T) {
	f := newLotteryFake()
	// enabled 在 DB 里是布尔 true：notice_get 是原样回传，必须是 true 而不是 1。
	f.rowFn = aboutSettingsFn("notice", `{"enabled":true,"content":"维护通知","extra":[1,2]}`)
	w := callAction(newLotteryRouter(f), "GET", "/api.php?action=notice_get", "", "", nil)
	want := `{"code":0,"msg":"ok","data":{"content":"维护通知","mode":"daily","enabled":true,"extra":[1,2]}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
	assertCORS(t, w.Header())
}

func TestNoticeGetDefaults(t *testing.T) {
	f := newLotteryFake()
	w := callAction(newLotteryRouter(f), "GET", "/api.php?action=notice_get", "", "", nil)
	want := `{"code":0,"msg":"ok","data":{"content":"","mode":"daily","enabled":0}}`
	if w.Body.String() != want {
		t.Fatalf("逐字不一致\n got=%s\nwant=%s", w.Body.String(), want)
	}
}

// TestAboutNoticeWritersStayPassthrough 写入侧不在本单范围，必须继续透传。
func TestAboutNoticeWritersStayPassthrough(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	for _, a := range []string{"about_config_set", "notice_set"} {
		if strings.Contains(code, `case "`+a+`":`) {
			t.Fatalf("%s 不该在本单原生接管（只搬读取）", a)
		}
	}
	for _, a := range []string{"about_config_get", "notice_get"} {
		if !strings.Contains(code, `case "`+a+`":`) {
			t.Fatalf("%s 未注册", a)
		}
	}
}
