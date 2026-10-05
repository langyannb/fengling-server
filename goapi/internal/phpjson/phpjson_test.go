package phpjson

import (
	"encoding/json"
	"testing"
)

func TestShellShape(t *testing.T) {
	got := string(Marshal(New().Set("code", 0).Set("msg", "ok").Set("data", nil)))
	want := `{"code":0,"msg":"ok","data":null}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestKeyOrderIsInsertionOrder(t *testing.T) {
	got := string(Marshal(New().Set("z", 1).Set("a", 2).Set("m", 3)))
	want := `{"z":1,"a":2,"m":3}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestUnicodeNotEscapedAndSlashEscaped(t *testing.T) {
	got := string(Marshal(New().Set("msg", "登录已失效").Set("url", "https://a.cn-x/b.apk")))
	want := `{"msg":"登录已失效","url":"https:\/\/a.cn-x\/b.apk"}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestHTMLEscapingDisabled(t *testing.T) {
	got := string(Marshal(New().Set("v", `<a href="x">&'`)))
	want := `{"v":"<a href=\"x\">&'"}` // 注意：`/` 不在这里；PHP 只转 `"` `\` `/` 与控制字符
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestControlChars(t *testing.T) {
	got := string(Marshal(New().Set("v", "a\nb\tc\r\x01")))
	want := `{"v":"a\nb\tc\r\u0001"}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestFloatsMatchPHP(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, `{"v":0.0}`},
		{12, `{"v":12.0}`},
		{12.5, `{"v":12.5}`},
		{0.1, `{"v":0.1}`},
		{1e21, `{"v":1.0e+21}`},
		{1e-5, `{"v":1.0e-5}`},
	}
	for _, c := range cases {
		got := string(Marshal(New().Set("v", c.in)))
		if got != c.want {
			t.Errorf("float %v: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestFloatsDifferFromEncodingJSON(t *testing.T) {
	// 这条是「为什么要自己写编码器」的回归断言：encoding/json 会把 12.0 输出成 12。
	raw, err := json.Marshal(map[string]float64{"v": 12})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"v":12}` {
		t.Fatalf("encoding/json 行为变了: %s", raw)
	}
	if got := string(Marshal(New().Set("v", float64(12)))); got != `{"v":12.0}` {
		t.Fatalf("phpjson got %s", got)
	}
}

// 用主 agent 从线上抓回来的**真实报文片段**做回归：斜杠必须转义成 \/，中文不转义。
func TestRealProductionVersionSample(t *testing.T) {
	got := string(Marshal(New().
		Set("code", 0).
		Set("msg", "ok").
		Set("data", New().
			Set("version", "1.1.14").
			Set("url", "https://fenglin.cn-nb1.rains3.com/apk/fengling_20261005_182034.apk").
			Set("update_log", "1. 视频消息: 支持发送视频"))))
	want := `{"code":0,"msg":"ok","data":{"version":"1.1.14",` +
		`"url":"https:\/\/fenglin.cn-nb1.rains3.com\/apk\/fengling_20261005_182034.apk",` +
		`"update_log":"1. 视频消息: 支持发送视频"}}`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestNumbersFromDatabaseJSON(t *testing.T) {
	got := string(Marshal(New().
		Set("a", json.Number("12")).
		Set("b", json.Number("12.5")).
		Set("c", json.Number("0"))))
	want := `{"a":12,"b":12.5,"c":0}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestNestedAndArrays(t *testing.T) {
	inner := New().Set("pm_id", 0).Set("group_id", 7)
	got := string(Marshal(New().Set("type", "hello").Set("data", New().
		Set("user_id", 3).Set("cursor", inner))))
	want := `{"type":"hello","data":{"user_id":3,"cursor":{"pm_id":0,"group_id":7}}}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	arr := string(Marshal(New().Set("tags", []any{"a", "b"}).Set("empty", []any{})))
	if arr != `{"tags":["a","b"],"empty":[]}` {
		t.Fatalf("array got %s", arr)
	}
}
