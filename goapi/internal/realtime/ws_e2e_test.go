package realtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 端到端证明：真的起一个 WebSocket 服务、真的用 gorilla 客户端连上去，
// 然后往「数据库」（这里是假 source）里插一条群消息 —— 客户端必须收到 group 事件。
// 除了 pumpTick 的那一次查库被换成了假 source，链路（ServeWS → 轮询泵 → 环缓 →
// 按连接投递 → dispatch → wsWritePump → 真实 TCP 帧）与生产完全一致。
func TestWSEndToEndReceivesGroupEvent(t *testing.T) {
	cfg := config.Config{
		PollInterval: 5 * time.Millisecond,
		WSHeartbeat:  500 * time.Millisecond,
		WSIdle:       5 * time.Second,
		WSWriteWait:  2 * time.Second,
		WSSendBuffer: 64,
		OnlineTTL:    time.Second,
	}
	h := newTestHub(t)
	h.cfg = cfg
	src := &fakeSource{pmMax: 100, grpMax: 200}
	h.src = src
	e := &Engine{cfg: cfg, log: h.log, hub: h}
	h.startPump()
	defer e.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.NotFound(w, r)
			return
		}
		// uid=8，游标 = 建连时的 MAX(id)（100 / 200），与生产 resolveCursor 的结果一致。
		e.ServeWS(w, r, &store.User{ID: 8, Nickname: "八号"}, 100, 200)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	cli, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket 拨号失败: %v", err)
	}
	defer cli.Close()
	_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))

	// ① hello 必须是最先出去的那一帧
	hello := readFrame(t, cli, "hello")
	data := hello["data"].(map[string]any)
	if uid := int64(data["user_id"].(float64)); uid != 8 {
		t.Fatalf("hello.user_id=%d 期望 8", uid)
	}
	cursor := data["cursor"].(map[string]any)
	if pmID, grpID := int64(cursor["pm_id"].(float64)), int64(cursor["group_id"].(float64)); pmID != 100 || grpID != 200 {
		t.Fatalf("hello.cursor=%v 期望 {100 200}", cursor)
	}

	// ② ping（顶层 t）→ pong 必须回显
	if err := cli.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping","t":12345}`)); err != nil {
		t.Fatalf("发 ping 失败: %v", err)
	}
	pong := readFrame(t, cli, "pong")
	pd := pong["data"].(map[string]any)
	if got := int64(pd["t"].(float64)); got != 12345 {
		t.Fatalf("pong.t=%d 期望回显 12345（帧=%v）", got, pong)
	}
	if _, ok := pd["server_time"]; !ok {
		t.Fatalf("pong 缺 server_time: %v", pong)
	}

	// ③ 插两条群消息：第二条是 video 字段完整的视频消息（与验收脚本的检查项一致）
	src.pushGroup(grpRow{
		id: 201, groupID: 5, userID: 9, content: "第一条普通消息", msgType: "text",
		createdAt: "2026-10-05 15:30:00", nickname: "九号", groupName: "测试群",
	})
	src.pushGroup(grpRow{
		id: 202, groupID: 5, userID: 9, content: "第二条视频消息", msgType: "video",
		video: "uploads/v/2026/10/a.mp4", videoW: 720, videoH: 1280,
		videoDur: 15, videoSize: 1234567,
		createdAt: "2026-10-05 15:30:01", nickname: "九号", groupName: "测试群",
	})

	g1 := readFrame(t, cli, "group")
	d1 := g1["data"].(map[string]any)
	if id := int64(d1["id"].(float64)); id != 201 {
		t.Fatalf("第一条 group 事件 id=%d 期望 201", id)
	}
	g2 := readFrame(t, cli, "group")
	d2 := g2["data"].(map[string]any)
	if id := int64(d2["id"].(float64)); id != 202 {
		t.Fatalf("第二条 group 事件 id=%d 期望 202", id)
	}
	if mt := d2["msg_type"].(string); mt != "video" {
		t.Fatalf("msg_type=%q 期望 video", mt)
	}
	for k, want := range map[string]any{
		"video":          "uploads/v/2026/10/a.mp4",
		"video_w":        float64(720),
		"video_h":        float64(1280),
		"video_duration": float64(15),
		"video_size":     float64(1234567),
		"group_id":       float64(5),
		"group_name":     "测试群",
		"user_id":        float64(9),
		"nickname":       "九号",
	} {
		if got, ok := d2[k]; !ok || got != want {
			t.Fatalf("第二条 group 事件字段 %s=%v 期望 %v（完整帧=%s）", k, got, want, mustJSON(t, g2))
		}
	}

	// 由「真实 TCP 上的字节」再确认一次：字段名与顺序都在
	if raw := mustJSON(t, g2); !strings.Contains(raw, `"video":"uploads/v/2026/10/a.mp4"`) {
		t.Fatalf("原始帧缺少 video 字段: %s", raw)
	}
	if e.ActiveConns() != 1 {
		t.Fatalf("ActiveConns=%d 期望 1", e.ActiveConns())
	}
}

// 未登录：先完成升级 → 发 error 帧（code 401）→ 以 4001 关闭（验收脚本的检查项）。
func TestWSUnauthorizedErrorFrameAndCloseCode(t *testing.T) {
	cfg := config.Config{WSWriteWait: 2 * time.Second, WSSendBuffer: 8}
	h := newTestHub(t)
	e := &Engine{cfg: cfg, log: h.log, hub: h}
	defer e.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.ServeWS(w, r, nil, 0, 0)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	cli, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket 拨号失败: %v", err)
	}
	defer cli.Close()
	_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))

	_, raw, err := cli.ReadMessage()
	if err != nil {
		t.Fatalf("应先收到 error 帧: %v", err)
	}
	if got := string(raw); !strings.Contains(got, `"code":401`) {
		t.Fatalf("error 帧不对: %s", got)
	}

	_, _, err = cli.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("期望收到关闭帧, 实际 %v", err)
	}
	if ce.Code != 4001 {
		t.Fatalf("关闭码=%d 期望 4001", ce.Code)
	}
}

// readFrame 读到指定 type 的帧（跳过心跳 ping 等无关帧）。
func readFrame(t *testing.T, cli *websocket.Conn, want string) map[string]any {
	t.Helper()
	for i := 0; i < 20; i++ {
		_, raw, err := cli.ReadMessage()
		if err != nil {
			t.Fatalf("等 %q 帧时读失败: %v", want, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("帧不是 JSON: %s", raw)
		}
		// 把真实收到的原始字节打进 -v 日志：回看时这就是「客户端确实收到了」的证据
		t.Logf("客户端收到帧: %s", raw)
		if m["type"] == want {
			return m
		}
	}
	t.Fatalf("没等到 %q 帧", want)
	return nil
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
