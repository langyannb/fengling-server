package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// 阶段 4（私聊 + 通知 + 匿名上报）单测。复用 native_social_test.go 的脚本化假数据访问层。
//
// 覆盖派单书点名要的八项：逐字文案 / 403 与 429 都落 HTTP 400 / video_meta_params 越界 /
// 建会话先于限速 / around>before>after 优先级 / 未读计数 / 撤回三类拒绝 / 空集合 [] 与 null 不混。

// migratedPmCols 模拟「私聊视频迁移已经跑过」的库（social_pm_messages 的 video / msg_type 都在）。
func migratedPmCols() fakeCols {
	return fakeCols{
		"social_pm_messages.video":    true,
		"social_pm_messages.msg_type": true,
	}
}

const pmToken = "tok-pm-0001"

// callPM 带 Bearer token 调一个 action（登录态）。
func callPM(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json",
		map[string]string{"Authorization": "Bearer " + pmToken})
}

// callAnon 匿名调一个 action（crash_report / harm_report）。
func callAnon(t *testing.T, rt *Router, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callAction(rt, "POST", "/api.php?action="+action, body, "application/json", nil)
}

func newPmRouter(db *socialFakeDB, cols columnProbe, video *store.VideoConfig) *Router {
	if cols == nil {
		cols = migratedPmCols()
	}
	return newSocialRouter(db, cols, video)
}

// ---------- pm_send：逐字文案与顺序 ----------

func TestPmSendValidations(t *testing.T) {
	base := func() *socialFakeDB {
		db := &socialFakeDB{me: socialMe()}
		db.rules = []socialRule{
			{match: "FROM social_user_mutes", rows: nil},
			{match: "SELECT id, username, nickname, avatar FROM users WHERE id = ?", rows: []store.Row{
				srow("id", 9, "username", "bob", "nickname", "小博", "avatar", ""),
			}},
		}
		db.vals = []valRule{
			{match: "SELECT id FROM social_pms WHERE user_a = ? AND user_b = ?", val: int64(42), ok: true},
			{match: "SELECT created_at FROM social_pm_messages WHERE from_user = ?", val: nil, ok: false},
		}
		return db
	}

	cases := []struct {
		name     string
		body     string
		cols     columnProbe
		video    *store.VideoConfig
		wantCode int
		wantMsg  string
	}{
		{name: "没有 to_user 也没有 conv_id", body: `{}`, wantCode: 1, wantMsg: "参数错误"},
		{name: "不能给自己发私聊", body: `{"to_user":7}`, wantCode: 1, wantMsg: "不能给自己发私聊"},
		{name: "内容为空", body: `{"to_user":9}`, wantCode: 1, wantMsg: "消息内容不能为空"},
		{name: "超过 500 字", body: `{"to_user":9,"content":"` + strings.Repeat("字", 501) + `"}`,
			wantCode: 1, wantMsg: "消息不能超过 500 个字"},
		{name: "图片地址不合法", body: `{"to_user":9,"image":"https://evil.example.com/chat/x.png"}`,
			wantCode: 1, wantMsg: "图片地址不合法"},
		{name: "视频地址不合法", body: `{"to_user":9,"video":"https://evil.example.com/chat/x.mp4"}`,
			wantCode: 1, wantMsg: "视频地址不合法"},
		{name: "视频尺寸越界（早于空内容校验）",
			body: `{"to_user":9,"video_w":10001}`, wantCode: 1, wantMsg: "视频尺寸参数不合法"},
		{name: "视频时长越界", body: `{"to_user":9,"video_duration":3601}`,
			wantCode: 1, wantMsg: "视频时长参数不合法 (最长 60 分钟)"},
		{name: "视频大小越界", body: `{"to_user":9,"video_size":2147483648}`,
			wantCode: 1, wantMsg: "视频大小参数不合法"},
		{name: "视频功能未开启", body: `{"to_user":9,"video":"` + testS3Public + `/chat/a.mp4"}`,
			video: &store.VideoConfig{Enabled: 0}, wantCode: 1, wantMsg: "视频消息功能未开启"},
		{name: "服务端未完成视频迁移", body: `{"to_user":9,"video":"` + testS3Public + `/chat/a.mp4"}`,
			cols: fakeCols{}, wantCode: 1, wantMsg: "服务端未完成视频迁移, 请联系管理员"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := base()
			rt := newPmRouter(db, tc.cols, tc.video)
			w := callPM(t, rt, "pm_send", tc.body)
			assertError(t, w, 400, tc.wantCode, tc.wantMsg)
		})
	}

	t.Run("未登录是 401（不是 400）", func(t *testing.T) {
		rt := newPmRouter(base(), nil, nil)
		w := callAnon(t, rt, "pm_send", `{"to_user":9,"content":"hi"}`)
		assertError(t, w, 401, 401, "登录已失效")
	})

	t.Run("全站禁言 403 落 HTTP 400", func(t *testing.T) {
		db := base()
		db.rules[0] = socialRule{match: "FROM social_user_mutes", rows: []store.Row{
			srow("id", 3, "group_id", 0, "until_at", nil, "reason", "广告"),
		}}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_send", `{"to_user":9,"content":"hi"}`)
		assertError(t, w, 400, 403, "你已被禁言 (永久), 原因: 广告")
	})

	t.Run("对方不存在或已被封禁", func(t *testing.T) {
		db := base()
		db.rules[1] = socialRule{match: "SELECT id, username, nickname, avatar FROM users WHERE id = ?", rows: nil}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_send", `{"to_user":9,"content":"hi"}`)
		assertError(t, w, 400, 1, "对方不存在或已被封禁")
	})

	t.Run("会话不存在（带 conv_id 但不是我参与的）", func(t *testing.T) {
		db := base()
		db.rules = append([]socialRule{{match: "SELECT * FROM social_pms WHERE id = ?", rows: []store.Row{
			srow("id", 42, "user_a", 8, "user_b", 9),
		}}}, db.rules...)
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_send", `{"conv_id":42,"content":"hi"}`)
		assertError(t, w, 400, 1, "会话不存在")
	})
}

// TestPmSendCreatesConversationBeforeRateLimit 钉死 pm_send 的顺序：
// 建会话在限速之前 —— 被限速拒绝时 social_pms 里也会多出一个空会话（PHP 就是这个行为）。
func TestPmSendCreatesConversationBeforeRateLimit(t *testing.T) {
	db := &socialFakeDB{me: socialMe()}
	db.rules = []socialRule{
		{match: "FROM social_user_mutes", rows: nil},
		{match: "SELECT id, username, nickname, avatar FROM users WHERE id = ?", rows: []store.Row{
			srow("id", 9, "username", "bob", "nickname", "小博", "avatar", ""),
		}},
	}
	// 会话第一次查不到（触发 INSERT），INSERT 之后再查就有 id 了。
	convHits := 0
	db.valFn = func(q string, args []any) (any, bool, error) {
		switch {
		case strings.Contains(q, "SELECT id FROM social_pms WHERE user_a = ? AND user_b = ?"):
			convHits++
			if convHits >= 2 {
				return int64(42), true, nil
			}
			return nil, false, nil
		case strings.Contains(q, "SELECT created_at FROM social_pm_messages WHERE from_user = ?"):
			// 刚刚才发过一条 -> 命中 2 秒限速
			return phpDateTime(time.Now()), true, nil
		}
		return nil, false, nil
	}

	rt := newPmRouter(db, nil, nil)
	w := callPM(t, rt, "pm_send", `{"to_user":9,"content":"hi"}`)
	assertError(t, w, 400, 1, "发送太快了, 请稍后再试")

	if got := db.execs("INSERT INTO social_pms "); len(got) != 1 {
		t.Fatalf("被限速拒绝时也必须已经建过会话（INSERT INTO social_pms），实际 %d 次；calls=%v", len(got), db.calls)
	}
	if got := db.execs("INSERT INTO social_pm_messages "); len(got) != 0 {
		t.Fatalf("被限速拒绝时不该写入消息，实际写入了 %d 次", len(got))
	}
}

// TestPmSendSuccess 覆盖列探测拼 INSERT、更新会话、notify_merge 与响应字段序。
func TestPmSendSuccess(t *testing.T) {
	db := &socialFakeDB{me: socialMe(), insertID: 100}
	db.rules = []socialRule{
		{match: "FROM social_user_mutes", rows: nil},
		{match: "SELECT id, username, nickname, avatar FROM users WHERE id = ?", rows: []store.Row{
			srow("id", 9, "username", "bob", "nickname", "小博", "avatar", ""),
		}},
	}
	db.vals = []valRule{
		{match: "SELECT id FROM social_pms WHERE user_a = ? AND user_b = ?", val: int64(42), ok: true},
		{match: "SELECT created_at FROM social_pm_messages WHERE from_user = ?", val: nil, ok: false},
		{match: "SELECT id FROM notifications WHERE user_id = ?", val: int64(0), ok: false},
	}

	rt := newPmRouter(db, nil, nil)
	w := callPM(t, rt, "pm_send", `{"to_user":9,"content":"  你好  "}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, `{"code":0,"msg":"ok","data":{"id":100,"conv_id":42,"created_at":"`) {
		t.Fatalf("成功响应字段序不对: %s", body)
	}

	ins := db.execs("INSERT INTO social_pm_messages ")
	if len(ins) != 1 {
		t.Fatalf("期望 1 次消息 INSERT，实际 %d", len(ins))
	}
	q := ins[0].Query
	for _, col := range []string{"conv_id", "from_user", "to_user", "content", "image", "image_w", "image_h",
		"video", "video_w", "video_h", "video_duration", "video_size", "msg_type", "is_recalled"} {
		if !strings.Contains(q, col) {
			t.Fatalf("INSERT 缺列 %s: %s", col, q)
		}
	}
	// 第 4 个参数是 content：PHP 的 trim 已生效
	if got := ins[0].Args[3]; got != "你好" {
		t.Fatalf("content 没做 trim: %#v", got)
	}
	if got := ins[0].Args[0]; got != int64(42) {
		t.Fatalf("conv_id 参数错: %#v", got)
	}
	if got := db.execs("UPDATE social_pms SET last_message_id = ?"); len(got) != 1 {
		t.Fatalf("必须更新 social_pms.last_message_id，实际 %d 次", len(got))
	}
	nm := db.queries("SELECT id FROM notifications WHERE user_id = ?")
	if len(nm) != 1 {
		t.Fatalf("notify_merge 必须查一次未读通知，实际 %d", len(nm))
	}
	nmIns := db.execs("INSERT INTO notifications")
	if len(nmIns) != 1 {
		t.Fatalf("未读通知不存在时必须 INSERT，实际 %d 次", len(nmIns))
	}
	if got := nmIns[0].Args[1]; got != "爱丽丝 给你发来私聊" {
		t.Fatalf("通知标题错: %#v", got)
	}
	if got := nmIns[0].Args[4]; got != "pm:42:100" {
		t.Fatalf("通知 link 错: %#v", got)
	}
}

// ---------- pm_messages：游标优先级、空集合、has_more ----------

func TestPmMessagesCursorPriority(t *testing.T) {
	me := socialMe()
	conv := srow("id", 42, "user_a", 7, "user_b", 9, "last_message_id", 0, "last_at", "")
	other := srow("id", 9, "username", "bob", "nickname", "", "avatar", "", "bio", "", "role", "user",
		"created_at", "2026-01-01 00:00:00", "tags", "")

	mk := func() *socialFakeDB {
		db := &socialFakeDB{me: me}
		db.rules = []socialRule{
			{match: "SELECT * FROM social_pms WHERE id = ?", rows: []store.Row{conv}},
			{match: "SELECT id, username, nickname, avatar, bio, role, created_at, tags FROM users WHERE id = ?",
				rows: []store.Row{other}},
		}
		db.allFn = func(q string, args []any) ([]store.Row, error) {
			if strings.Contains(q, "FROM social_pm_messages m") {
				return nil, nil // 无未读
			}
			if strings.Contains(q, "FROM social_pm_messages") {
				return []store.Row{srow("id", 100, "conv_id", 42, "from_user", 9, "to_user", 7,
					"content", "hi", "image", "", "image_w", 0, "image_h", 0, "is_recalled", 0,
					"created_at", "2026-10-05 10:00:00")}, nil
			}
			return nil, nil
		}
		db.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM social_pm_messages WHERE conv_id = ? AND id < ?") {
				return int64(2), true, nil
			}
			return nil, false, nil
		}
		return db
	}

	cases := []struct {
		name     string
		body     string
		wantSub  string
		wantArgs []any
	}{
		{name: "around 最高优先", body: `{"conv_id":42,"around_id":10,"before_id":5,"after_id":3}`,
			wantSub: "id <= ?", wantArgs: []any{int64(42), int64(10)}},
		{name: "around 缺省时 before 优先", body: `{"conv_id":42,"before_id":5,"after_id":3}`,
			wantSub: "id < ?", wantArgs: []any{int64(42), int64(5)}},
		{name: "只有 after", body: `{"conv_id":42,"after_id":3}`,
			wantSub: "id > ?", wantArgs: []any{int64(42), int64(3)}},
		{name: "都没有就是最新一页", body: `{"conv_id":42}`,
			wantSub: "conv_id = ? ORDER BY id DESC", wantArgs: []any{int64(42)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := mk()
			rt := newPmRouter(db, nil, nil)
			w := callPM(t, rt, "pm_messages", tc.body)
			if w.Code != 200 {
				t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
			}
			qs := db.queries("SELECT * FROM social_pm_messages WHERE conv_id = ?")
			if len(qs) != 1 {
				t.Fatalf("期望 1 次消息查询，实际 %d：%v", len(qs), db.calls)
			}
			if !strings.Contains(qs[0].Query, tc.wantSub) {
				t.Fatalf("游标优先级错：SQL=%s 期望含 %q", qs[0].Query, tc.wantSub)
			}
			if len(qs[0].Args) != len(tc.wantArgs) {
				t.Fatalf("参数个数错: %#v", qs[0].Args)
			}
			for i := range tc.wantArgs {
				if qs[0].Args[i] != tc.wantArgs[i] {
					t.Fatalf("参数[%d]=%#v 期望 %#v", i, qs[0].Args[i], tc.wantArgs[i])
				}
			}
			d := dataOf(t, w)
			if d["has_more_before"] != float64(1) {
				t.Fatalf("has_more_before 应为 1: %#v", d)
			}
			if d["mine"] != nil {
				t.Fatal("pm_messages 不该有 mine 顶层字段")
			}
			// other 只输出这 8 个键
			o, _ := d["other"].(map[string]any)
			if len(o) != 8 {
				t.Fatalf("other 字段数=%d 期望 8: %#v", len(o), o)
			}
			if o["nickname"] != "bob" {
				t.Fatalf("nickname 空时要回落 username: %#v", o["nickname"])
			}
		})
	}
}

// TestPmMessagesEmptyCollections 钉死 [] 与 null 不许混。
func TestPmMessagesEmptyCollections(t *testing.T) {
	t.Run("会话推导不出来：other=null、list=[]", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_messages", `{}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d", w.Code)
		}
		want := `{"code":0,"msg":"ok","data":{"conv_id":0,"other":null,"list":[],"has_more_before":0,"unread":0,"first_unread_id":0,"my_id":7}}`
		if got := w.Body.String(); got != want {
			t.Fatalf("逐字不一致\n got=%s\nwant=%s", got, want)
		}
	})

	t.Run("会话存在但没有消息：list=[]", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		db.rules = []socialRule{
			{match: "SELECT * FROM social_pms WHERE id = ?", rows: []store.Row{
				srow("id", 42, "user_a", 7, "user_b", 9)}},
			{match: "FROM users WHERE id = ?", rows: []store.Row{srow("id", 9, "username", "bob")}},
		}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_messages", `{"conv_id":42}`)
		if !strings.Contains(w.Body.String(), `"list":[]`) {
			t.Fatalf("空列表必须是 []：%s", w.Body.String())
		}
	})

	t.Run("会话不存在报 会话不存在", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_messages", `{"conv_id":42}`)
		assertError(t, w, 400, 1, "会话不存在")
	})
}

// ---------- pm_conversations：未读计数与 last=null ----------

func TestPmConversationsListAndUnread(t *testing.T) {
	db := &socialFakeDB{me: socialMe()}
	db.rules = []socialRule{
		{match: "FROM social_pms c", rows: []store.Row{
			srow("id", 42, "user_a", 7, "user_b", 9, "last_message_id", 100, "last_at", "2026-10-05 10:00:00",
				"a_id", 7, "a_username", "alice", "a_nickname", "爱丽丝", "a_avatar", "", "a_tags", "",
				"b_id", 9, "b_username", "bob", "b_nickname", "", "b_avatar", "b.png", "b_tags", "vip"),
			srow("id", 43, "user_a", 9, "user_b", 7, "last_message_id", 0, "last_at", "",
				"a_id", 9, "a_username", "bob", "a_nickname", "", "a_avatar", "", "a_tags", "",
				"b_id", 7, "b_username", "alice", "b_nickname", "爱丽丝", "b_avatar", "", "b_tags", ""),
		}},
		{match: "FROM social_pm_messages m", rows: []store.Row{
			srow("conv_id", 42, "c", 3, "first_id", 101),
		}},
		{match: "SELECT * FROM social_pm_messages WHERE id = ?", rows: []store.Row{
			srow("id", 100, "conv_id", 42, "from_user", 9, "to_user", 7, "content", "hi",
				"image", "", "image_w", 0, "image_h", 0, "is_recalled", 0, "created_at", "2026-10-05 10:00:00"),
		}},
	}
	rt := newPmRouter(db, nil, nil)
	w := callPM(t, rt, "pm_conversations", `{}`)
	if w.Code != 200 {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total_unread":3`) {
		t.Fatalf("total_unread 应为所有会话未读之和: %s", body)
	}
	d := dataOf(t, w)
	list, _ := d["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list 长度=%d", len(list))
	}
	first, _ := list[0].(map[string]any)
	if first["unread"] != float64(3) || first["first_unread_id"] != float64(101) {
		t.Fatalf("第 1 个会话未读错: %#v", first)
	}
	u, _ := first["user"].(map[string]any)
	if len(u) != 5 || u["nickname"] != "bob" {
		t.Fatalf("user_brief 应收敛为 5 键且 nickname 回落 username: %#v", u)
	}
	if !strings.Contains(body, `"last":null`) {
		t.Fatalf("没有消息的会话 last 必须是 null: %s", body)
	}
	if !strings.Contains(body, `"last":{"id":100`) {
		t.Fatalf("有消息的会话 last 应是 pm_msg_public 结构: %s", body)
	}
}

// ---------- pm_read：未读归零 + GREATEST ----------

func TestPmReadAndUnreadReset(t *testing.T) {
	db := &socialFakeDB{me: socialMe()}
	db.rules = []socialRule{
		{match: "SELECT * FROM social_pms WHERE id = ?", rows: []store.Row{
			srow("id", 42, "user_a", 7, "user_b", 9)}},
	}
	db.valFn = func(q string, args []any) (any, bool, error) {
		switch {
		case strings.Contains(q, "COALESCE(MAX(id), 0)"):
			return int64(150), true, nil
		case strings.Contains(q, "SELECT last_read_id FROM social_pm_reads"):
			return int64(150), true, nil
		}
		return nil, false, nil
	}
	rt := newPmRouter(db, nil, nil)

	t.Run("last_id 缺省时取 MAX(id)", func(t *testing.T) {
		w := callPM(t, rt, "pm_read", `{"conv_id":42}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		ins := db.execs("INSERT INTO social_pm_reads")
		if len(ins) != 1 {
			t.Fatalf("期望 1 次已读写入，实际 %d", len(ins))
		}
		if !strings.Contains(ins[0].Query, "GREATEST(last_read_id, VALUES(last_read_id))") {
			t.Fatalf("已读必须用 GREATEST 不回退: %s", ins[0].Query)
		}
		if got := ins[0].Args[2]; got != int64(150) {
			t.Fatalf("last_read_id 参数错: %#v", got)
		}
		want := `{"code":0,"msg":"ok","data":{"ok":true,"conv_id":42,"last_read_id":150}}`
		if got := w.Body.String(); got != want {
			t.Fatalf("逐字不一致\n got=%s\nwant=%s", got, want)
		}
	})

	t.Run("conv_id 必须 > 0", func(t *testing.T) {
		w := callPM(t, rt, "pm_read", `{}`)
		assertError(t, w, 400, 1, "参数错误")
	})

	t.Run("不是我参与的会话", func(t *testing.T) {
		bad := &socialFakeDB{me: socialMe()}
		bad.rules = []socialRule{{match: "SELECT * FROM social_pms WHERE id = ?", rows: []store.Row{
			srow("id", 42, "user_a", 8, "user_b", 9)}}}
		w := callPM(t, newPmRouter(bad, nil, nil), "pm_read", `{"conv_id":42}`)
		assertError(t, w, 400, 1, "会话不存在")
	})
}

// ---------- pm_recall：三类拒绝 ----------

func TestPmRecallRejections(t *testing.T) {
	now := phpDateTime(time.Now())
	old := phpDateTime(time.Now().Add(-10 * time.Minute))

	t.Run("消息不存在", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_recall", `{"id":1}`)
		assertError(t, w, 400, 1, "消息不存在")
	})

	t.Run("已经撤回过", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		db.rules = []socialRule{{match: "SELECT * FROM social_pm_messages WHERE id = ?", rows: []store.Row{
			srow("id", 1, "from_user", 7, "to_user", 9, "is_recalled", 1, "created_at", now)}}}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_recall", `{"id":1}`)
		assertError(t, w, 400, 1, "消息已经撤回了")
	})

	t.Run("不是本人也不是管理员 -> 403 落 HTTP 400", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		db.rules = []socialRule{{match: "SELECT * FROM social_pm_messages WHERE id = ?", rows: []store.Row{
			srow("id", 1, "from_user", 8, "to_user", 7, "is_recalled", 0, "created_at", now)}}}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_recall", `{"id":1}`)
		assertError(t, w, 400, 403, "没有权限撤回这条消息")
	})

	t.Run("本人但超过 5 分钟", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		db.rules = []socialRule{{match: "SELECT * FROM social_pm_messages WHERE id = ?", rows: []store.Row{
			srow("id", 1, "from_user", 7, "to_user", 9, "is_recalled", 0, "created_at", old)}}}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_recall", `{"id":1}`)
		assertError(t, w, 400, 1, "只能撤回 5 分钟内的消息")
	})

	t.Run("本人且在 5 分钟内 -> 成功", func(t *testing.T) {
		db := &socialFakeDB{me: socialMe()}
		db.rules = []socialRule{{match: "SELECT * FROM social_pm_messages WHERE id = ?", rows: []store.Row{
			srow("id", 1, "from_user", 7, "to_user", 9, "is_recalled", 0, "created_at", now)}}}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_recall", `{"id":1}`)
		if w.Code != 200 || w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true}}` {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		if got := db.execs("UPDATE social_pm_messages SET is_recalled = 1"); len(got) != 1 {
			t.Fatalf("必须落一次撤回 UPDATE，实际 %d", len(got))
		}
	})

	t.Run("管理员可撤回他人的超时消息", func(t *testing.T) {
		db := &socialFakeDB{me: socialAdmin()}
		db.rules = []socialRule{{match: "SELECT * FROM social_pm_messages WHERE id = ?", rows: []store.Row{
			srow("id", 1, "from_user", 8, "to_user", 9, "is_recalled", 0, "created_at", old)}}}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "pm_recall", `{"id":1}`)
		if w.Code != 200 {
			t.Fatalf("管理员应能撤回: HTTP=%d body=%s", w.Code, w.Body.String())
		}
	})
}

// ---------- notifications ----------

func TestNotificationActions(t *testing.T) {
	mk := func() *socialFakeDB {
		db := &socialFakeDB{me: socialMe()}
		db.allFn = func(q string, args []any) ([]store.Row, error) {
			switch {
			case strings.Contains(q, "SELECT type, COUNT(*) AS c"):
				return []store.Row{
					srow("type", "system", "c", 2),
					srow("type", "pm", "c", 2),
				}, nil
			case strings.Contains(q, "SELECT * FROM notifications"):
				return []store.Row{
					srow("id", 9, "user_id", 7, "title", "标题", "content", "正文", "type", "system",
						"link", "", "is_read", 0, "created_at", "2026-10-05 09:00:00"),
				}, nil
			}
			return nil, nil
		}
		// 计数查询是「先 total 再 unread」两次（unread_only=1 时两条 SQL 完全一样，
		// 所以只能按调用次序区分，不能按 SQL 文本区分）。
		cntCalls := 0
		db.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM notifications") {
				cntCalls++
				if cntCalls == 1 {
					return int64(9), true, nil
				}
				return int64(4), true, nil
			}
			return nil, false, nil
		}
		return db
	}

	t.Run("列表：分页、未读分类与字段序", func(t *testing.T) {
		db := mk()
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "notifications", `{"page":2,"page_size":10,"unread_only":1}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		want := `{"code":0,"msg":"ok","data":{"list":[{"id":9,"title":"标题","content":"正文","type":"system","link":"","is_read":0,"created_at":"2026-10-05 09:00:00"}],` +
			`"total":9,"unread":4,"unread_by_type":{"system":2,"pm":2},"page":2,"page_size":10}}`
		if body != want {
			t.Fatalf("逐字不一致\n got=%s\nwant=%s", body, want)
		}
		// unread_only=1 会进 cond；OFFSET = (2-1)*10
		ls := db.queries("SELECT * FROM notifications")
		if len(ls) != 1 || !strings.Contains(ls[0].Query, " AND is_read = 0 ORDER BY id DESC LIMIT 10 OFFSET 10") {
			t.Fatalf("分页/条件拼接错: %+v", ls)
		}
	})

	t.Run("unread_by_type 空的时候必须是 [] 而不是 {}", func(t *testing.T) {
		db := mk()
		db.allFn = func(q string, args []any) ([]store.Row, error) { return nil, nil }
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "notifications", `{}`)
		if !strings.Contains(w.Body.String(), `"unread_by_type":[]`) {
			t.Fatalf("空分类必须是 []：%s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"list":[]`) {
			t.Fatalf("空列表必须是 []：%s", w.Body.String())
		}
	})

	t.Run("page 与 page_size 的下界与上界", func(t *testing.T) {
		db := mk()
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "notifications", `{"page":-3,"page_size":999}`)
		d := dataOf(t, w)
		if d["page"] != float64(1) || d["page_size"] != float64(50) {
			t.Fatalf("clamp 错: %#v", d)
		}
	})

	t.Run("notification_read all=1", func(t *testing.T) {
		db := mk()
		// notification_read 只发一条未读计数查询（没有 total），单独钉住它。
		db.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM notifications") {
				return int64(4), true, nil
			}
			return nil, false, nil
		}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "notification_read", `{"all":1}`)
		if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true,"unread":4}}` {
			t.Fatalf("逐字不一致: %s", w.Body.String())
		}
		got := db.execs("UPDATE notifications SET is_read = 1 WHERE user_id = ?")
		if len(got) != 1 || len(got[0].Args) != 1 {
			t.Fatalf("all=1 应按 user_id 更新: %+v", got)
		}
	})

	t.Run("notification_read id=0 报参数错误", func(t *testing.T) {
		rt := newPmRouter(mk(), nil, nil)
		w := callPM(t, rt, "notification_read", `{}`)
		assertError(t, w, 400, 1, "参数错误")
	})

	t.Run("notification_read 单条按 id + user_id", func(t *testing.T) {
		db := mk()
		db.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "SELECT COUNT(*) FROM notifications") {
				return int64(4), true, nil
			}
			return nil, false, nil
		}
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "notification_read", `{"id":9}`)
		if w.Code != 200 {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		got := db.execs("UPDATE notifications SET is_read = 1 WHERE id = ? AND user_id = ?")
		if len(got) != 1 || len(got[0].Args) != 2 {
			t.Fatalf("单条已读必须带 user_id 兜底: %+v", got)
		}
	})

	t.Run("notification_delete", func(t *testing.T) {
		db := mk()
		rt := newPmRouter(db, nil, nil)
		w := callPM(t, rt, "notification_delete", `{"id":9}`)
		if w.Body.String() != `{"code":0,"msg":"ok","data":{"ok":true}}` {
			t.Fatalf("逐字不一致: %s", w.Body.String())
		}
		got := db.execs("DELETE FROM notifications WHERE id = ? AND user_id = ?")
		if len(got) != 1 {
			t.Fatalf("删除必须带 user_id 兜底: %+v", got)
		}
	})

	t.Run("notification_delete id=0 报参数错误", func(t *testing.T) {
		rt := newPmRouter(mk(), nil, nil)
		w := callPM(t, rt, "notification_delete", `{}`)
		assertError(t, w, 400, 1, "参数错误")
	})

	t.Run("未登录 401", func(t *testing.T) {
		rt := newPmRouter(mk(), nil, nil)
		for _, action := range []string{"notifications", "notification_read", "notification_delete"} {
			w := callAnon(t, rt, action, `{}`)
			assertError(t, w, 401, 401, "登录已失效")
		}
	})
}

// ---------- 匿名上报 ----------

func TestCrashReportAction(t *testing.T) {
	t.Run("stack 为空 -> code 400 且 HTTP 400", func(t *testing.T) {
		db := &socialFakeDB{}
		rt := newPmRouter(db, nil, nil)
		w := callAnon(t, rt, "crash_report", `{"device":"pixel"}`)
		assertError(t, w, 400, 400, "stack 为空")
	})

	t.Run("超长 stack 截断到 20000 字", func(t *testing.T) {
		db := &socialFakeDB{}
		rt := newPmRouter(db, nil, nil)
		stack := strings.Repeat("a", 25000)
		body, _ := json.Marshal(map[string]any{"stack": stack, "device": strings.Repeat("d", 300)})
		w := callAnon(t, rt, "crash_report", string(body))
		if w.Code != 200 || w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		got := db.execs("INSERT INTO crash_reports")
		if len(got) != 1 {
			t.Fatalf("期望 1 次 INSERT，实际 %d", len(got))
		}
		if len(got[0].Args) != 5 {
			t.Fatalf("列数错: %#v", got[0].Args)
		}
		dev, _ := got[0].Args[0].(string)
		if len(dev) != 100 {
			t.Fatalf("device 应截断到 100 字，实际 %d", len(dev))
		}
		stk, _ := got[0].Args[3].(string)
		if len(stk) != 20000 {
			t.Fatalf("stack 应截断到 20000 字，实际 %d", len(stk))
		}
		if got[0].Args[4] != "192.0.2.1" {
			t.Fatalf("ip 取错: %#v", got[0].Args[4])
		}
	})
}

func TestHarmReportAction(t *testing.T) {
	t.Run("内容为空（含全空白）", func(t *testing.T) {
		db := &socialFakeDB{}
		rt := newPmRouter(db, nil, nil)
		w := callAnon(t, rt, "harm_report", `{"content":"   "}`)
		assertError(t, w, 400, 400, "请填写哪里被和谐了")
	})

	t.Run("同 IP 60 秒内重复 -> 429 落 HTTP 400", func(t *testing.T) {
		db := &socialFakeDB{}
		db.valFn = func(q string, args []any) (any, bool, error) {
			if strings.Contains(q, "INTERVAL 60 SECOND") {
				return int64(1), true, nil
			}
			return nil, false, nil
		}
		rt := newPmRouter(db, nil, nil)
		w := callAnon(t, rt, "harm_report", `{"content":"这首歌被和谐了"}`)
		assertError(t, w, 400, 429, "提交太频繁，请稍后再试")
	})

	t.Run("同 IP 同软件 10 分钟内重复 -> 429 落 HTTP 400", func(t *testing.T) {
		db := &socialFakeDB{}
		db.valFn = func(q string, args []any) (any, bool, error) {
			switch {
			case strings.Contains(q, "INTERVAL 60 SECOND"):
				return int64(0), true, nil
			case strings.Contains(q, "INTERVAL 600 SECOND"):
				return int64(2), true, nil
			}
			return nil, false, nil
		}
		rt := newPmRouter(db, nil, nil)
		w := callAnon(t, rt, "harm_report", `{"content":"x","app_id":33}`)
		assertError(t, w, 400, 429, "该软件刚刚反馈过，请勿重复提交")
	})

	t.Run("成功：内容截断 500 / contact 截断 100 / data 为 null", func(t *testing.T) {
		db := &socialFakeDB{}
		rt := newPmRouter(db, nil, nil)
		payload, _ := json.Marshal(map[string]any{
			"content":  strings.Repeat("哈", 600),
			"contact":  strings.Repeat("c", 150),
			"app_id":   33,
			"app_name": strings.Repeat("n", 150),
		})
		w := callAnon(t, rt, "harm_report", string(payload))
		if w.Code != 200 || w.Body.String() != `{"code":0,"msg":"ok","data":null}` {
			t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
		}
		got := db.execs("INSERT INTO harm_reports")
		if len(got) != 1 {
			t.Fatalf("期望 1 次 INSERT，实际 %d", len(got))
		}
		if got[0].Args[0] != int64(33) {
			t.Fatalf("app_id 参数错: %#v", got[0].Args[0])
		}
		name, _ := got[0].Args[1].(string)
		if len(name) != 100 {
			t.Fatalf("app_name 应截断到 100 字，实际 %d", len(name))
		}
		content, _ := got[0].Args[2].(string)
		if len([]rune(content)) != 500 {
			t.Fatalf("content 应截断到 500 字，实际 %d", len([]rune(content)))
		}
		contact, _ := got[0].Args[3].(string)
		if len(contact) != 100 {
			t.Fatalf("contact 应截断到 100 字，实际 %d", len(contact))
		}
	})
}
