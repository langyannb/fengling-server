package realtime

import (
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// SSE 帧的字节格式必须与 PHP `sse_event()`（api.php:3779）一字不差：
// `event: <名>\ndata: <json>\n\n`
func TestWriteEventExactBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	ev := event{name: "pm", data: phpjson.New().
		Set("id", 7).
		Set("content", "看这个 https://a.cn/b.apk").
		Set("video_size", int64(0))}
	if ok := writeEvent(rec, rec, ev); !ok {
		t.Fatal("writeEvent 返回失败")
	}
	want := "event: pm\n" +
		"data: {\"id\":7,\"content\":\"看这个 https:\\/\\/a.cn\\/b.apk\",\"video_size\":0}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

// retry 行、心跳注释行、bye 收尾行都必须是固定字节。
func TestSSEFixedFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRaw(rec, rec, "retry: 2000\n\n") // 连上先发（api.php:3675）
	writeRaw(rec, rec, ": hb\n\n")        // 每 10 秒（api.php:3692）
	writeRaw(rec, rec, byeFrame)          // 25 秒到点（api.php:3688）
	want := "retry: 2000\n\n: hb\n\n" + byeFrame
	if got := rec.Body.String(); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

// 缓冲满了必须丢弃并计数，绝不阻塞 hub（契约第 5 节）。
func TestDispatchDropsWhenBufferFull(t *testing.T) {
	h := &Hub{
		cfg:   config.Config{WSSendBuffer: 1},
		log:   slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		conns: map[*conn]struct{}{},
	}
	c := &conn{id: 1, userID: 9, kind: "ws", send: make(chan []byte, 1), closed: make(chan struct{})}
	h.dispatch(c, []byte("first"))
	h.dispatch(c, []byte("second")) // 缓冲已满 -> 丢弃并计数
	if got := c.dropped.Load(); got != 1 {
		t.Fatalf("dropped=%d, 期望 1", got)
	}
	if got := <-c.send; string(got) != "first" {
		t.Fatalf("队列内容=%s", got)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// at_users 的 PHP 语义：`(int)trim($v) === 0` 会把空段/非数字段也当成「@所有人」。
func TestPHPAtoiAtUsers(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0}, {"0", 0}, {"abc", 0}, {"-3", -3}, {"+4x", 4}, {"7", 7},
		// phpAtoi 只负责 PHP 的 (int) 语义，不 trim —— 调用方照 PHP 先 trim(explode(',', …)) 再传进来。
		{" 12 ", 0},
	}
	for _, tc := range cases {
		if got := phpAtoi(tc.in); got != tc.want {
			t.Errorf("phpAtoi(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}
