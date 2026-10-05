package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
)

// newTestHub 构造一个不连 MySQL 的 Hub（src 由各用例注入假实现）。
func newTestHub(t *testing.T) *Hub {
	t.Helper()
	return &Hub{
		cfg: config.Config{
			WSSendBuffer: 64,
			PollInterval: 5 * time.Millisecond,
			WSHeartbeat:  15 * time.Second,
			WSIdle:       60 * time.Second,
			WSWriteWait:  10 * time.Second,
			OnlineTTL:    time.Second,
		},
		log:     slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		conns:   make(map[*conn]struct{}),
		pmRing:  newRing(ringCap),
		grpRing: newRing(ringCap),
	}
}

// drain 取空某条连接已经排队的帧（非阻塞）。
func drain(c *conn) []string {
	var out []string
	for {
		select {
		case f := <-c.send:
			out = append(out, string(f))
		default:
			return out
		}
	}
}

// collectFrames 等 connection 上凑齐 n 帧（最多等 timeout）。
func collectFrames(t *testing.T, c *conn, n int, timeout time.Duration) []string {
	t.Helper()
	var out []string
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case f := <-c.send:
			out = append(out, string(f))
		case <-deadline:
			t.Fatalf("等第 %d 帧超时, 只收到 %v", len(out)+1, out)
		}
	}
	return out
}

// fakeSource 是 source 的假实现：单测里验证水位推进 / 过滤 / 投递，不需要 MySQL。
// 行可以在泵运行时追加（pushPM / pushGroup），所以内部带锁（-race 下也干净）。
type fakeSource struct {
	mu            sync.Mutex
	pmMax, grpMax int64
	pm            []pmRow
	grp           []grpRow

	pmCalls  atomic.Int64
	grpCalls atomic.Int64
}

// pushPM / pushGroup 在泵跑着的时候追加一行（模拟「数据库里新来了一条消息」）。
func (f *fakeSource) pushPM(r pmRow) {
	f.mu.Lock()
	f.pm = append(f.pm, r)
	f.mu.Unlock()
}

func (f *fakeSource) pushGroup(r grpRow) {
	f.mu.Lock()
	f.grp = append(f.grp, r)
	f.mu.Unlock()
}

func (f *fakeSource) maxID(_ context.Context, table string) (int64, error) {
	if table == "social_pm_messages" {
		return f.pmMax, nil
	}
	return f.grpMax, nil
}

func (f *fakeSource) newPM(_ context.Context, since int64) ([]pmRow, error) {
	f.pmCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pmRow
	for _, r := range f.pm {
		if r.id > since {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeSource) newGroup(_ context.Context, since int64) ([]grpRow, error) {
	f.grpCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []grpRow
	for _, r := range f.grp {
		if r.id > since {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---------- 环形缓冲 ----------

// 容量 3、推 5 条：只留最后 3 条，且 since 按 id 升序返回。
func TestRingDropsOldest(t *testing.T) {
	r := newRing(3)
	for i := int64(1); i <= 5; i++ {
		r.push(i, pmRow{id: i})
	}
	got := r.since(-1)
	if len(got) != 3 {
		t.Fatalf("缓冲长度=%d, 期望 3", len(got))
	}
	for i, e := range got {
		want := int64(i + 3) // 3,4,5
		if e.id != want {
			t.Fatalf("第 %d 条 id=%d 期望 %d", i, e.id, want)
		}
	}
	if last := r.lastID(); last != 5 {
		t.Fatalf("lastID=%d 期望 5", last)
	}
	if got := r.since(4); len(got) != 1 || got[0].id != 5 {
		t.Fatalf("since(4)=%v 期望只剩 id=5", got)
	}
	if got := r.since(5); len(got) != 0 {
		t.Fatalf("since(5)=%v 期望空", got)
	}
}

// ---------- 渲染（必须与 PHP sse_event 字段名/顺序/类型一字不差） ----------

func TestRenderPMExactBytes(t *testing.T) {
	row := pmRow{
		id: 7, convID: 3, fromUser: 9, toUser: 8,
		content: "你好/世界", image: "", nickname: "小明", username: "u9",
		video: "a/b.mp4", videoW: 720, videoH: 1280, videoDur: 15, videoSize: 123456,
		createdAt: "2026-10-05 15:29:24",
	}
	ev, ok := row.renderPM(8)
	if !ok {
		t.Fatal("收件人本人应该收到")
	}
	want := `{"id":7,"conv_id":3,"from_user":9,"nickname":"小明","username":"u9",` +
		`"content":"你好\/世界","image":"","msg_type":"video","video":"a\/b.mp4",` +
		`"video_w":720,"video_h":1280,"video_duration":15,"video_size":123456,` +
		`"created_at":"2026-10-05 15:29:24"}`
	if got := string(phpjson.Marshal(ev.data)); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if ev.name != "pm" {
		t.Fatalf("事件名=%s", ev.name)
	}
}

func TestRenderPMSkipsSelfAndOthers(t *testing.T) {
	if _, ok := (pmRow{id: 1, fromUser: 8, toUser: 8}).renderPM(8); ok {
		t.Fatal("自己发的 pm 不该推")
	}
	if _, ok := (pmRow{id: 2, fromUser: 9, toUser: 7}).renderPM(8); ok {
		t.Fatal("发给别人的 pm 不该推给 8")
	}
	ev, ok := (pmRow{id: 3, fromUser: 9, toUser: 8}).renderPM(8)
	if !ok {
		t.Fatal("应该推")
	}
	got := string(phpjson.Marshal(ev.data))
	if !strings.Contains(got, `"nickname":"用户9"`) || !strings.Contains(got, `"msg_type":""`) {
		t.Fatalf("nickname/msg_type 回落不对: %s", got)
	}
}

func TestRenderGroupExactBytes(t *testing.T) {
	row := grpRow{
		id: 12, groupID: 5, userID: 9,
		content: "@所有人 看这个", image: "", atUsers: "8,0,", msgType: "text",
		createdAt: "2026-10-05 15:30:00", groupName: "测试群",
	}
	ev, ok := row.renderGroup(8, map[int64]bool{5: true})
	if !ok {
		t.Fatal("别人发的群消息应该推")
	}
	// at_users="8,0," -> 末尾空段按 PHP 语义也算「@所有人」，所以 at_me=1 / at_all=1
	want := `{"id":12,"group_id":5,"group_name":"测试群","user_id":9,"nickname":"用户9",` +
		`"content":"@所有人 看这个","image":"","video":"","video_w":0,"video_h":0,` +
		`"video_duration":0,"video_size":0,"at_me":1,"at_all":1,"muted":1,"msg_type":"text",` +
		`"created_at":"2026-10-05 15:30:00"}`
	if got := string(phpjson.Marshal(ev.data)); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if ev.name != "group" {
		t.Fatalf("事件名=%s", ev.name)
	}

	ev, _ = row.renderGroup(7, nil)
	got := string(phpjson.Marshal(ev.data))
	if !strings.Contains(got, `"at_me":0,"at_all":1,"muted":0`) {
		t.Fatalf("按收件人计算不对: %s", got)
	}

	if _, ok := (grpRow{id: 13, groupID: 5, userID: 7}).renderGroup(7, nil); ok {
		t.Fatal("自己发的群消息不该推")
	}
}

// ---------- 投递：按连接游标 + 按收件人过滤 + 游标推进到全局水位 ----------

func TestDeliverOnceFiltersAndAdvancesCursor(t *testing.T) {
	h := newTestHub(t)
	a := h.add(8, "ws", 100, 200)
	b := h.add(9, "ws", 100, 200)
	// 一条 SSE 连接不该被 WS 的泵投递
	s := h.add(7, "sse", 100, 200)

	h.evMu.Lock()
	h.pmWater, h.grpWater = 103, 203
	h.pmRing.push(101, pmRow{id: 101, convID: 1, fromUser: 9, toUser: 8, content: "hi"})
	h.pmRing.push(102, pmRow{id: 102, convID: 1, fromUser: 7, toUser: 9, content: "yo"})
	h.pmRing.push(103, pmRow{id: 103, convID: 1, fromUser: 8, toUser: 8, content: "self"})
	h.grpRing.push(201, grpRow{id: 201, groupID: 5, userID: 9, content: "g1"})
	h.grpRing.push(202, grpRow{id: 202, groupID: 5, userID: 8, content: "self-g"})
	h.grpRing.push(203, grpRow{id: 203, groupID: 6, userID: 7, content: "g2"})
	h.evMu.Unlock()

	h.deliverOnce(context.Background(), time.Now())

	// A(uid=8)：pm 只有 101（103 是自己发的），group 有 201 和 203（不做成员过滤）
	fa := drain(a)
	if len(fa) != 3 {
		t.Fatalf("A 收到 %d 帧: %v", len(fa), fa)
	}
	if !strings.HasPrefix(fa[0], `{"type":"pm","data":{"id":101,`) {
		t.Fatalf("A 第一帧不是 pm 101: %s", fa[0])
	}
	if !strings.HasPrefix(fa[1], `{"type":"group","data":{"id":201,`) ||
		!strings.HasPrefix(fa[2], `{"type":"group","data":{"id":203,`) {
		t.Fatalf("A 的 group 帧顺序不对: %v", fa[1:])
	}

	// B(uid=9)：pm 只有 102；group 201 是自己发的 -> 跳过；
	// 202（8 发的）与 203（7 发的）都要收到 —— 阶段 0 不做成员过滤，与 PHP 一致。
	fb := drain(b)
	if len(fb) != 3 {
		t.Fatalf("B 收到 %d 帧: %v", len(fb), fb)
	}
	if !strings.HasPrefix(fb[0], `{"type":"pm","data":{"id":102,`) ||
		!strings.HasPrefix(fb[1], `{"type":"group","data":{"id":202,`) ||
		!strings.HasPrefix(fb[2], `{"type":"group","data":{"id":203,`) {
		t.Fatalf("B 的帧不对: %v", fb)
	}

	// 无论推没推，WS 连接的游标都必须推进到全局水位（否则自己发消息时会漏掉别人的事件）
	for _, c := range []*conn{a, b} {
		if got := c.pmCur.Load(); got != 103 {
			t.Fatalf("conn %d pmCur=%d 期望 103", c.id, got)
		}
		if got := c.grpCur.Load(); got != 203 {
			t.Fatalf("conn %d grpCur=%d 期望 203", c.id, got)
		}
	}
	// SSE 连接由它自己的循环负责，既不该收到 WS 泵的帧，游标也不该被泵改动
	if f := drain(s); len(f) != 0 {
		t.Fatalf("SSE 连接收到了 WS 泵的帧: %v", f)
	}
	if got := s.pmCur.Load(); got != 100 {
		t.Fatalf("SSE 连接 pmCur 被泵改动: %d", got)
	}
	if got := s.grpCur.Load(); got != 200 {
		t.Fatalf("SSE 连接 grpCur 被泵改动: %d", got)
	}
}

// 客户端显式续订（游标 > 全局水位）时绝不能把游标往回拨，也不能补推历史。
func TestDeliverOnceNeverRewindsCursor(t *testing.T) {
	h := newTestHub(t)
	c := h.add(8, "ws", 500, 600)
	h.evMu.Lock()
	h.pmWater, h.grpWater = 103, 203
	h.pmRing.push(101, pmRow{id: 101, fromUser: 9, toUser: 8})
	h.evMu.Unlock()

	h.deliverOnce(context.Background(), time.Now())

	if f := drain(c); len(f) != 0 {
		t.Fatalf("不该补推历史: %v", f)
	}
	if got := c.pmCur.Load(); got != 500 {
		t.Fatalf("pmCur 被回拨成 %d", got)
	}
	if got := c.grpCur.Load(); got != 600 {
		t.Fatalf("grpCur 被回拨成 %d", got)
	}
}

// 免打扰集合按连接用户缓存，10 秒内不重复查库。
func TestMutedCacheTTL(t *testing.T) {
	h := newTestHub(t)
	c := h.add(8, "ws", 0, 0)
	now := time.Now()
	if got := h.mutedFor(context.Background(), c, now); len(got) != 0 {
		t.Fatal("db 为 nil 时应为空集合")
	}
	c.mutedMu.Lock()
	if !c.mutedAt.Equal(now) {
		t.Fatalf("首次应写入时间戳, mutedAt=%v", c.mutedAt)
	}
	c.mutedMu.Unlock()

	_ = h.mutedFor(context.Background(), c, now.Add(mutedTTL-time.Millisecond))
	c.mutedMu.Lock()
	if !c.mutedAt.Equal(now) {
		t.Fatalf("10 秒内不该刷新, mutedAt=%v", c.mutedAt)
	}
	c.mutedMu.Unlock()

	_ = h.mutedFor(context.Background(), c, now.Add(mutedTTL+time.Millisecond))
	c.mutedMu.Lock()
	if !c.mutedAt.After(now) {
		t.Fatalf("过期后应刷新, mutedAt=%v", c.mutedAt)
	}
	c.mutedMu.Unlock()
}

// ---------- 轮询泵：端到端（假 source，不需要 MySQL） ----------

// 有 WS 连接时泵才查库；查到的行推进全局水位并真的投到 WS 连接上。
func TestPumpLoopDeliversToWSConn(t *testing.T) {
	h := newTestHub(t)
	src := &fakeSource{
		pmMax: 100, grpMax: 200,
		pm:  []pmRow{{id: 101, convID: 1, fromUser: 9, toUser: 8, content: "新私信"}},
		grp: []grpRow{{id: 201, groupID: 5, userID: 9, content: "新群消息", msgType: "text"}},
	}
	h.src = src
	h.startPump()
	t.Cleanup(h.stopPump)
	h.startPump() // 幂等：重复调用不该起第二个泵

	// 只有 SSE 连接时不许查库（对齐「没有 WS 连接不要空跑数据库查询」）
	h.add(7, "sse", 100, 200)
	time.Sleep(60 * time.Millisecond)
	if n := src.pmCalls.Load(); n != 0 {
		t.Fatalf("只有 SSE 连接时不该查 pm 表, 实际 %d 次", n)
	}

	// 接入一条 WS 连接（游标 = 建连时的 MAX(id)，即 100/200）
	c := h.add(8, "ws", 100, 200)

	got := collectFrames(t, c, 2, 3*time.Second)
	if !strings.HasPrefix(got[0], `{"type":"pm","data":{"id":101,`) {
		t.Fatalf("第一帧应为 pm 101: %s", got[0])
	}
	if !strings.HasPrefix(got[1], `{"type":"group","data":{"id":201,`) ||
		!strings.Contains(got[1], `"content":"新群消息"`) {
		t.Fatalf("第二帧应为 group 201: %s", got[1])
	}
	h.evMu.Lock()
	pm, grp := h.pmWater, h.grpWater
	h.evMu.Unlock()
	if pm != 101 {
		t.Fatalf("pm 全局水位=%d 期望 101", pm)
	}
	if grp != 201 {
		t.Fatalf("group 全局水位=%d 期望 201", grp)
	}
	if cur := c.pmCur.Load(); cur != 101 {
		t.Fatalf("连接 pmCur=%d 期望 101", cur)
	}
	if cur := c.grpCur.Load(); cur != 201 {
		t.Fatalf("连接 grpCur=%d 期望 201", cur)
	}
}

// 水位为 0（启动时取水位失败）时，泵必须重新取一次 MAX(id)，
// 绝不从 0 开始「从头往回捞」。
func TestPumpDoesNotReplayFromZero(t *testing.T) {
	h := newTestHub(t)
	src := &fakeSource{pmMax: 900, grpMax: 900}
	h.src = src
	h.add(8, "ws", 900, 900)

	h.pumpTick(context.Background())

	h.evMu.Lock()
	pm, grp := h.pmWater, h.grpWater
	h.evMu.Unlock()
	if pm != 900 || grp != 900 {
		t.Fatalf("水位补取失败: pm=%d grp=%d", pm, grp)
	}
}

// ---------- 协议：pong 回显 ----------

func TestPongFrameEcho(t *testing.T) {
	jr := func(s string) json.RawMessage {
		if s == "" {
			return nil
		}
		return json.RawMessage(s)
	}
	cases := []struct {
		name string
		top  string
		data string
		now  int64
		want string
	}{
		{"顶层 t 回显", "12345", "", 1700000000,
			`{"type":"pong","data":{"t":12345,"server_time":1700000000}}`},
		{"顶层浮点也接受", "12345.0", "", 1700000000,
			`{"type":"pong","data":{"t":12345,"server_time":1700000000}}`},
		{"data.t 回显", "", `{"t":777}`, 1700000000,
			`{"type":"pong","data":{"t":777,"server_time":1700000000}}`},
		{"顶层优先于 data", "1", `{"t":2}`, 5,
			`{"type":"pong","data":{"t":1,"server_time":5}}`},
		{"缺 t 用服务器时间", "", "", 1700000000,
			`{"type":"pong","data":{"t":1700000000,"server_time":1700000000}}`},
		{"t 非法用服务器时间", "", `{"t":"abc"}`, 42,
			`{"type":"pong","data":{"t":42,"server_time":42}}`},
		{"顶层非法但 data.t 可用", `"abc"`, `{"t":9}`, 5,
			`{"type":"pong","data":{"t":9,"server_time":5}}`},
		{"data 不是对象", "7", `[1,2]`, 5,
			`{"type":"pong","data":{"t":7,"server_time":5}}`},
	}
	for _, tc := range cases {
		got := string(pongFrame(jr(tc.top), jr(tc.data), tc.now))
		if got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// ---------- SQL 文本（防止与 PHP 漂移） ----------

// SSE 查询与全局泵查询必须只差收件人条件：列清单完全一致（否则共用 scanPM 会错位）。
func TestQueryStringsExact(t *testing.T) {
	for _, useVideo := range []bool{false, true} {
		pmConn, pmAll := pmQueryConn(useVideo), pmQueryAll(useVideo)
		if a, b := sqlCols(pmConn), sqlCols(pmAll); a != b {
			t.Fatalf("useVideo=%v pm 列清单不一致:\n%s\n%s", useVideo, a, b)
		}
		grpConn, grpAll := grpQueryConn(useVideo), grpQueryAll(useVideo)
		if a, b := sqlCols(grpConn), sqlCols(grpAll); a != b {
			t.Fatalf("useVideo=%v group 列清单不一致:\n%s\n%s", useVideo, a, b)
		}
		if !strings.Contains(pmConn, "WHERE m.to_user = ? AND m.id > ? AND m.is_recalled = 0") {
			t.Fatalf("pmConn WHERE 不对: %s", pmConn)
		}
		if strings.Contains(pmAll, "m.to_user = ?") {
			t.Fatalf("pmAll 不该带收件人条件: %s", pmAll)
		}
		if !strings.Contains(pmAll, "WHERE m.id > ? AND m.is_recalled = 0") {
			t.Fatalf("pmAll WHERE 不对: %s", pmAll)
		}
		if !strings.Contains(grpConn, "WHERE m.id > ? AND m.user_id <> ? AND m.is_recalled = 0") {
			t.Fatalf("grpConn WHERE 不对: %s", grpConn)
		}
		if strings.Contains(grpAll, "m.user_id <>") {
			t.Fatalf("grpAll 不该带发送人条件: %s", grpAll)
		}
		if !strings.Contains(pmConn, "ORDER BY m.id ASC LIMIT 20") ||
			!strings.Contains(grpAll, "ORDER BY m.id ASC LIMIT 30") {
			t.Fatal("LIMIT / 排序与 PHP 不一致")
		}
		if useVideo {
			if !strings.Contains(pmAll, vselVideo) || !strings.Contains(grpAll, vselVideo) {
				t.Fatal("有 video 列时应拼上视频列")
			}
		} else if strings.Contains(pmAll, "m.video") || strings.Contains(grpAll, "m.video") {
			t.Fatal("没有 video 列时不得出现视频列")
		}
	}
}

// sqlCols 取 SELECT 到 WHERE 之间的列片段（压缩空白后比较）。
func sqlCols(q string) string {
	i := strings.Index(q, "WHERE")
	if i < 0 {
		return q
	}
	return strings.Join(strings.Fields(q[:i]), " ")
}
