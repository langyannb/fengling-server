// Package realtime 实现原生 SSE 与 WebSocket 之间的公共部分：
// 连接注册表（内存 hub）、Redis 在线状态、以及**事件来源**。
//
// 事件来源是与 PHP 完全相同的 1 秒 DB 轮询，但两条通道的取数方式不同：
//   - SSE 连接在**自己的循环**里同步轮询（pollOnce → 事件直接写进响应体，逐字对齐 PHP）；
//   - WS 连接没有自己的循环，由**全局轮询泵**统一查库（每类事件一个环形缓冲），
//     再按每条连接的游标投递（pumpLoop / deliverOnce）。
//
// 两条路径的**渲染代码是同一份**（pmRow.renderPM / grpRow.renderGroup），所以字段名、
// 字段顺序、字符串/整型类型天然一致；环缓里存的是「原始行」而不是渲染好的 JSON，
// 因为 at_me / at_all / muted 是**按收件人**算的。
//
// 阶段 3/4 换成 Redis Pub/Sub 时，只要把 mysqlSource 换成订阅 fls:events 的实现，
// 把消息喂进同一个环形缓冲即可，SSE/WS 侧都不用改。
package realtime

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/langyannb/fengling-server/goapi/internal/config"
	"github.com/langyannb/fengling-server/goapi/internal/phpjson"
	"github.com/langyannb/fengling-server/goapi/internal/store"
)

// event 是一次投递：SSE 事件名 + 数据体（WS 侧包成 {"type":name,"data":data}）。
type event struct {
	name string
	data *phpjson.O
}

// conn 是一条已建立的实时连接（SSE 或 WS）。
type conn struct {
	id     uint64
	userID int64
	kind   string // "sse" | "ws"

	pmCur  atomic.Int64
	grpCur atomic.Int64

	// send 只给 WS 用（SSE 是同步写，不需要队列）。
	send    chan []byte
	dropped atomic.Int64

	// muted / mutedAt 是 WS 投递时按收件人算免打扰用的缓存（10 秒过期，
	// 与 PHP 的 $mutedIds 在心跳时刷新同语义）。只有轮询泵读写它。
	mutedMu sync.Mutex
	muted   map[int64]bool
	mutedAt time.Time

	closed chan struct{}
	once   sync.Once
	// retired 保证「连接生命周期结束」只上报一次：closeAll 与处理 goroutine 的 defer
	// 会各调一次 remove，但只有**最后收尾的那次**才算连接真正结束。
	retired sync.Once
}

// Hub 是内存连接注册表 + WS 的全局事件轮询泵。
type Hub struct {
	cfg config.Config
	db  *store.Store
	rd  *store.Redis
	log *slog.Logger

	mu    sync.Mutex
	conns map[*conn]struct{}
	seq   atomic.Uint64

	// connWG 统计在途连接的处理 goroutine。Engine.Close 等它归零，
	// 从而保证所有连接的收尾动作（含 Redis MarkOffline）都跑完。
	connWG sync.WaitGroup

	// 轮询泵（startPump / stopPump 幂等）。
	pumpStart  sync.Once
	pumpMu     sync.Mutex
	pumpCancel context.CancelFunc
	pumpWG     sync.WaitGroup

	// src 是轮询泵的数据来源（生产上是 mysqlSource；单测可注入假实现）。
	src source

	// evMu 保护全局水位与两个环形缓冲（只有轮询泵写，投递时读）。
	evMu     sync.Mutex
	pmWater  int64
	grpWater int64
	pmRing   *ring
	grpRing  *ring
}

func newHub(cfg config.Config, db *store.Store, rd *store.Redis, log *slog.Logger) *Hub {
	return &Hub{
		cfg:     cfg,
		db:      db,
		rd:      rd,
		log:     log,
		conns:   make(map[*conn]struct{}),
		src:     mysqlSource{db: db},
		pmRing:  newRing(ringCap),
		grpRing: newRing(ringCap),
	}
}

func (h *Hub) add(userID int64, kind string, pmCur, grpCur int64) *conn {
	if h.cfg.WSSendBuffer <= 0 {
		h.cfg.WSSendBuffer = 64
	}
	c := &conn{
		id:     h.seq.Add(1),
		userID: userID,
		kind:   kind,
		send:   make(chan []byte, h.cfg.WSSendBuffer),
		closed: make(chan struct{}),
	}
	c.pmCur.Store(pmCur)
	c.grpCur.Store(grpCur)
	h.mu.Lock()
	h.conns[c] = struct{}{}
	n := len(h.conns)
	h.mu.Unlock()
	// 在**建立它的那个 goroutine**里登记生命周期，remove() 时上报结束。
	h.connWG.Add(1)
	h.log.Info("实时连接建立", "conn", c.id, "uid", userID, "kind", kind, "active", n)
	return c
}

// remove 由连接的处理 goroutine 在 defer 里调用（幂等）：摘注册表 + 上报生命周期结束。
//
// 它在 sse.go / ws.go 的 defer 链里**早于** rd.MarkOffline 注册，LIFO 下 MarkOffline
// 一定先跑完 —— 所以 Engine.Close 等 connWG 归零就等于「等所有连接把 MarkOffline 跑完」。
func (h *Hub) remove(c *conn) {
	h.mu.Lock()
	_, ok := h.conns[c]
	delete(h.conns, c)
	n := len(h.conns)
	h.mu.Unlock()
	if ok {
		h.log.Info("实时连接关闭", "conn", c.id, "uid", c.userID, "kind", c.kind,
			"active", n, "dropped", c.dropped.Load())
	}
	h.signal(c)
	c.retired.Do(func() { h.connWG.Done() })
}

// signal 幂等地给一条连接发退出信号（幂等靠 conn.once）。
func (h *Hub) signal(c *conn) {
	c.once.Do(func() { close(c.closed) })
}

func (h *Hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// wsCount 当前在途 WS 连接数（轮询泵只在 > 0 时才真的查库）。
func (h *Hub) wsCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for c := range h.conns {
		if c.kind == "ws" {
			n++
		}
	}
	return n
}

func (h *Hub) snapshot() []*conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	all := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		all = append(all, c)
	}
	return all
}

// closeAll 进程退出时用：给所有在途连接发退出信号（**不**摘注册表，
// 摘除与生命周期结束由各自处理 goroutine 的 defer 完成）。
func (h *Hub) closeAll() {
	for _, c := range h.snapshot() {
		h.signal(c)
	}
}

// waitConns 等所有在途连接的处理 goroutine 收尾，最多等 timeout。
// 超时返回 false（调用方记日志继续，绝不挂死）。
func (h *Hub) waitConns(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		h.connWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// dispatch 非阻塞投递一帧给某条连接（缓冲 64，满则丢弃并计数，绝不阻塞 hub）。
func (h *Hub) dispatch(c *conn, frame []byte) {
	select {
	case c.send <- frame:
	default:
		n := c.dropped.Add(1)
		// 只记第 1 次和每 100 次，避免刷日志。
		if n == 1 || n%100 == 0 {
			h.log.Warn("WS 发送缓冲已满, 丢弃事件", "conn", c.id, "uid", c.userID, "dropped", n)
		}
	}
}

// wsEventFrame 把一条事件包成 WS 帧：{"type":<事件名>,"data":<事件体>}
// （与 hello 帧同形，字段序固定）。
func wsEventFrame(ev event) []byte {
	return phpjson.Marshal(phpjson.New().
		Set("type", ev.name).
		Set("data", ev.data))
}

// ---------- 事件来源：1 秒轮询（与 PHP sse_run 完全同构） ----------

const (
	pmLimit  = 20
	grpLimit = 30
	// vselVideo 与 API 里 db_has_column('…','video') 为真时拼上去的片段一字不差。
	vselVideo = ", m.video, m.video_w, m.video_h, m.video_duration, m.video_size"
)

// videoCols 返回视频列片段（迁移没跑时为空的兼容行为）。
func videoCols(useVideo bool) string {
	if useVideo {
		return vselVideo
	}
	return ""
}

// pmQueryConn 是 SSE 单连接用的私信查询（对齐 api.php:3705 一字不差）。
// 注意 m.to_user 也在列里：WS 的全局查询要按收件人过滤，共用 scanPM 就必须列序一致。
func pmQueryConn(useVideo bool) string {
	return `SELECT m.id, m.conv_id, m.from_user, m.to_user, m.content, m.image` + videoCols(useVideo) + `, m.created_at,
	                 u.nickname, u.username
	            FROM social_pm_messages m JOIN users u ON u.id = m.from_user
	           WHERE m.to_user = ? AND m.id > ? AND m.is_recalled = 0
	           ORDER BY m.id ASC LIMIT ` + strconv.Itoa(pmLimit)
}

// pmQueryAll 是 WS 全局轮询泵用的私信查询：**只去掉收件人条件**
// （收件人过滤挪到投递时按连接做），列清单/排序/LIMIT/撤回过滤与 PHP 一字不差。
func pmQueryAll(useVideo bool) string {
	return `SELECT m.id, m.conv_id, m.from_user, m.to_user, m.content, m.image` + videoCols(useVideo) + `, m.created_at,
	                 u.nickname, u.username
	            FROM social_pm_messages m JOIN users u ON u.id = m.from_user
	           WHERE m.id > ? AND m.is_recalled = 0
	           ORDER BY m.id ASC LIMIT ` + strconv.Itoa(pmLimit)
}

// grpQueryConn 是 SSE 单连接用的群消息查询（对齐 api.php:3741 一字不差，
// 含 `m.user_id <> :me`：阶段 0 不做成员过滤，只对齐 PHP 的既有行为）。
func grpQueryConn(useVideo bool) string {
	return grpQueryPrefix(useVideo) + `
	           WHERE m.id > ? AND m.user_id <> ? AND m.is_recalled = 0
	           ORDER BY m.id ASC LIMIT ` + strconv.Itoa(grpLimit)
}

// grpQueryAll 是 WS 全局轮询泵用的群消息查询：只去掉 `m.user_id <> :me`
// （自己发的在投递时跳过，游标照旧推进），其余与 PHP 一字不差。
func grpQueryAll(useVideo bool) string {
	return grpQueryPrefix(useVideo) + `
	           WHERE m.id > ? AND m.is_recalled = 0
	           ORDER BY m.id ASC LIMIT ` + strconv.Itoa(grpLimit)
}

func grpQueryPrefix(useVideo bool) string {
	return `SELECT m.id, m.group_id, m.user_id, m.content, m.image, m.at_users, m.msg_type` + videoCols(useVideo) + `, m.created_at,
	                 u.nickname, g.name AS group_name
	            FROM social_messages m
	            JOIN users u ON u.id = m.user_id
	            LEFT JOIN social_groups g ON g.id = m.group_id`
}

// pollOnce 对应 sse_run 一轮循环里的「pm 查询 + group 查询」。
// emit 返回 false 表示下游写失败（客户端已断开），立刻停止本轮。
func (h *Hub) pollOnce(ctx context.Context, c *conn, muted map[int64]bool, emit func(event) bool) {
	if ctx.Err() != nil {
		return
	}
	h.pollPM(ctx, c, emit)
	if ctx.Err() != nil {
		return
	}
	h.pollGroup(ctx, c, muted, emit)
}

// pollPM 对齐 api.php:3698-3727。渲染与 WS 投递共用 pmRow.renderPM。
func (h *Hub) pollPM(ctx context.Context, c *conn, emit func(event) bool) {
	useVideo := h.db.HasColumn(ctx, "social_pm_messages", "video")
	cur := c.pmCur.Load()
	rows, err := h.db.DB().QueryContext(ctx, pmQueryConn(useVideo), c.userID, cur)
	if err != nil {
		h.log.Warn("pm 轮询失败", "conn", c.id, "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanPM(rows, useVideo)
		if err != nil {
			h.log.Warn("pm 行解析失败", "conn", c.id, "err", err)
			return
		}
		// 先推进游标，再判断是不是自己发的（与 PHP 顺序一致）
		if row.id > cur {
			cur = row.id
			c.pmCur.Store(row.id)
		}
		ev, ok := row.renderPM(c.userID)
		if !ok {
			continue
		}
		if !emit(ev) {
			return
		}
	}
	if err := rows.Err(); err != nil {
		h.log.Warn("pm 轮询中断", "conn", c.id, "err", err)
	}
}

// pollGroup 对齐 api.php:3729-3772。渲染与 WS 投递共用 grpRow.renderGroup。
func (h *Hub) pollGroup(ctx context.Context, c *conn, muted map[int64]bool, emit func(event) bool) {
	useVideo := h.db.HasColumn(ctx, "social_messages", "video")
	cur := c.grpCur.Load()
	rows, err := h.db.DB().QueryContext(ctx, grpQueryConn(useVideo), cur, c.userID)
	if err != nil {
		h.log.Warn("group 轮询失败", "conn", c.id, "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanGroup(rows, useVideo)
		if err != nil {
			h.log.Warn("group 行解析失败", "conn", c.id, "err", err)
			return
		}
		if row.id > cur {
			cur = row.id
			c.grpCur.Store(row.id)
		}
		ev, ok := row.renderGroup(c.userID, muted)
		if !ok {
			continue
		}
		if !emit(ev) {
			return
		}
	}
	if err := rows.Err(); err != nil {
		h.log.Warn("group 轮询中断", "conn", c.id, "err", err)
	}
}

// ---------- 原始行 + 按收件人渲染 ----------

// pmRow 是 social_pm_messages 的一行原始数据（渲染推迟到投递时做）。
type pmRow struct {
	id        int64
	convID    int64
	fromUser  int64
	toUser    int64
	content   string
	image     string
	video     string
	videoW    int64
	videoH    int64
	videoDur  int64
	videoSize int64
	createdAt string
	nickname  string
	username  string
}

// grpRow 是 social_messages 的一行原始数据（at_users 留着，投递时按收件人算 at_me/at_all）。
type grpRow struct {
	id        int64
	groupID   int64
	userID    int64
	content   string
	image     string
	atUsers   string
	msgType   string
	video     string
	videoW    int64
	videoH    int64
	videoDur  int64
	videoSize int64
	createdAt string
	nickname  string
	groupName string
}

// scanPM 按 pmQueryConn / pmQueryAll 的列序扫描（两个查询列序完全一致）。
func scanPM(rows *sql.Rows, useVideo bool) (pmRow, error) {
	var (
		r                                   pmRow
		content, image, createdAt           sql.NullString
		nickname, username                  sql.NullString
		video                               sql.NullString
		videoW, videoH, videoDur, videoSize sql.NullInt64
	)
	var err error
	if useVideo {
		err = rows.Scan(&r.id, &r.convID, &r.fromUser, &r.toUser, &content, &image,
			&video, &videoW, &videoH, &videoDur, &videoSize,
			&createdAt, &nickname, &username)
	} else {
		err = rows.Scan(&r.id, &r.convID, &r.fromUser, &r.toUser, &content, &image,
			&createdAt, &nickname, &username)
	}
	if err != nil {
		return r, err
	}
	r.content, r.image, r.createdAt = content.String, image.String, createdAt.String
	r.nickname, r.username = nickname.String, username.String
	r.video = video.String
	r.videoW, r.videoH, r.videoDur, r.videoSize = videoW.Int64, videoH.Int64, videoDur.Int64, videoSize.Int64
	return r, nil
}

// scanGroup 按 grpQueryConn / grpQueryAll 的列序扫描（两个查询列序完全一致）。
func scanGroup(rows *sql.Rows, useVideo bool) (grpRow, error) {
	var (
		r                                   grpRow
		content, image, atUsers, msgType    sql.NullString
		video                               sql.NullString
		videoW, videoH, videoDur, videoSize sql.NullInt64
		createdAt, nickname, groupName      sql.NullString
	)
	var err error
	if useVideo {
		err = rows.Scan(&r.id, &r.groupID, &r.userID, &content, &image, &atUsers, &msgType,
			&video, &videoW, &videoH, &videoDur, &videoSize,
			&createdAt, &nickname, &groupName)
	} else {
		err = rows.Scan(&r.id, &r.groupID, &r.userID, &content, &image, &atUsers, &msgType,
			&createdAt, &nickname, &groupName)
	}
	if err != nil {
		return r, err
	}
	r.content, r.image, r.atUsers, r.msgType = content.String, image.String, atUsers.String, msgType.String
	r.createdAt, r.nickname, r.groupName = createdAt.String, nickname.String, groupName.String
	r.video = video.String
	r.videoW, r.videoH, r.videoDur, r.videoSize = videoW.Int64, videoH.Int64, videoDur.Int64, videoSize.Int64
	return r, nil
}

// renderPM 渲染一条 pm 事件（字段名/顺序/类型必须与 PHP sse_event 一字不差）。
// ok=false 表示这条消息不该推给 uid：不是它的收件人，或者就是它自己发的
// （PHP 语义：游标照样先推进，只是不推送）。
func (r pmRow) renderPM(uid int64) (event, bool) {
	if r.toUser != uid || r.fromUser == uid {
		return event{}, false
	}
	nick := r.nickname
	if nick == "" {
		nick = "用户" + strconv.FormatInt(r.fromUser, 10)
	}
	msgType := ""
	if r.video != "" {
		msgType = "video"
	}
	d := phpjson.New().
		Set("id", r.id).
		Set("conv_id", r.convID).
		Set("from_user", r.fromUser).
		Set("nickname", nick).
		Set("username", r.username).
		Set("content", r.content).
		Set("image", r.image).
		Set("msg_type", msgType).
		Set("video", r.video).
		Set("video_w", r.videoW).
		Set("video_h", r.videoH).
		Set("video_duration", r.videoDur).
		Set("video_size", r.videoSize).
		Set("created_at", r.createdAt)
	return event{name: "pm", data: d}, true
}

// renderGroup 渲染一条 group 事件。at_me / at_all / muted 全部按 uid 现场计算
// （所以环形缓冲里只能存原始行）。ok=false 表示是 uid 自己发的。
func (r grpRow) renderGroup(uid int64, muted map[int64]bool) (event, bool) {
	if r.userID == uid {
		return event{}, false
	}
	atMe, atAll := 0, 0
	// 逐字复刻 PHP：`$v = (int)trim($v); if ($v === 0) $atAll = 1;`
	// 所以空串或非数字段（例如 at_users 结尾多了个逗号）都会把 at_all 置 1 —— 这是
	// 客户端已经适配的既有行为，必须照抄，不能「顺手修好」。
	if r.atUsers != "" {
		for _, seg := range strings.Split(r.atUsers, ",") {
			v := phpAtoi(strings.TrimSpace(seg))
			if v == 0 {
				atAll = 1
			}
			if v > 0 && v == uid {
				atMe = 1
			}
		}
	}
	nick := r.nickname
	if nick == "" {
		nick = "用户" + strconv.FormatInt(r.userID, 10)
	}
	mutedFlag := 0
	if muted[r.groupID] {
		mutedFlag = 1
	}
	d := phpjson.New().
		Set("id", r.id).
		Set("group_id", r.groupID).
		Set("group_name", r.groupName).
		Set("user_id", r.userID).
		Set("nickname", nick).
		Set("content", r.content).
		Set("image", r.image).
		Set("video", r.video).
		Set("video_w", r.videoW).
		Set("video_h", r.videoH).
		Set("video_duration", r.videoDur).
		Set("video_size", r.videoSize).
		Set("at_me", atMe).
		Set("at_all", atAll).
		Set("muted", mutedFlag).
		Set("msg_type", r.msgType).
		Set("created_at", r.createdAt)
	return event{name: "group", data: d}, true
}

// phpAtoi 复刻 PHP 的 (int) 强转（用于 at_users 分段）。
func phpAtoi(s string) int64 {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == i {
		return 0
	}
	n, err := strconv.ParseInt(s[:j], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ---------- WS 全局轮询泵 ----------

const (
	// ringCap 是每类事件环形缓冲的容量，写满丢最旧。
	ringCap = 500
	// mutedTTL 是 WS 连接缓存免打扰群的过期时间（SSE 那边是心跳刷新，同为 10 秒）。
	mutedTTL = 10 * time.Second
)

// ringEntry 是环形缓冲里的一条原始行（**不**渲染成 JSON：at_me/at_all/muted 按收件人算）。
type ringEntry struct {
	id  int64
	row any // pmRow 或 grpRow
}

// ring 是「按 id 升序追加、容量固定、写满丢最旧」的环形缓冲（pm / group 各一个）。
type ring struct {
	cap  int
	buf  []ringEntry
	head int // 下一个写入位置
	size int
}

func newRing(capacity int) *ring {
	if capacity <= 0 {
		capacity = 1
	}
	return &ring{cap: capacity, buf: make([]ringEntry, capacity)}
}

// push 追加一条；满了覆盖最旧的一条。
func (r *ring) push(id int64, row any) {
	if r == nil || r.cap <= 0 {
		return
	}
	r.buf[r.head] = ringEntry{id: id, row: row}
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

// since 按 id 升序返回所有 id > cur 的条目（已被丢弃的自然不会返回）。
func (r *ring) since(cur int64) []ringEntry {
	if r == nil || r.size == 0 {
		return nil
	}
	out := make([]ringEntry, 0, r.size)
	for i := 0; i < r.size; i++ {
		e := r.buf[(r.head-r.size+i+r.cap)%r.cap]
		if e.id > cur {
			out = append(out, e)
		}
	}
	return out
}

// lastID 返回缓冲里最大的 id（空缓冲返回 0），测试用。
func (r *ring) lastID() int64 {
	if r == nil || r.size == 0 {
		return 0
	}
	return r.buf[(r.head-1+r.cap)%r.cap].id
}

// source 是轮询泵的最小数据来源面：生产实现是 mysqlSource（走 *store.Store），
// 单测可以注入假实现，从而在没有 MySQL 的环境里验证水位推进、过滤与投递。
type source interface {
	maxID(ctx context.Context, table string) (int64, error)
	newPM(ctx context.Context, since int64) ([]pmRow, error)
	newGroup(ctx context.Context, since int64) ([]grpRow, error)
}

// mysqlSource 是生产用的 source。
type mysqlSource struct {
	db *store.Store
}

func (s mysqlSource) maxID(ctx context.Context, table string) (int64, error) {
	return s.db.MaxID(ctx, table)
}

func (s mysqlSource) newPM(ctx context.Context, since int64) ([]pmRow, error) {
	useVideo := s.db.HasColumn(ctx, "social_pm_messages", "video")
	rows, err := s.db.DB().QueryContext(ctx, pmQueryAll(useVideo), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pmRow
	for rows.Next() {
		row, err := scanPM(rows, useVideo)
		if err != nil {
			return out, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s mysqlSource) newGroup(ctx context.Context, since int64) ([]grpRow, error) {
	useVideo := s.db.HasColumn(ctx, "social_messages", "video")
	rows, err := s.db.DB().QueryContext(ctx, grpQueryAll(useVideo), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grpRow
	for rows.Next() {
		row, err := scanGroup(rows, useVideo)
		if err != nil {
			return out, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// startPump 启动全局轮询泵（幂等，重复调用无副作用）。
func (h *Hub) startPump() {
	if h.src == nil {
		return
	}
	h.pumpStart.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		h.pumpMu.Lock()
		h.pumpCancel = cancel
		h.pumpMu.Unlock()
		h.pumpWG.Add(1)
		go func() {
			defer h.pumpWG.Done()
			h.pumpLoop(ctx)
		}()
	})
}

// stopPump 停掉轮询泵并等它退出（幂等；没启动过时立即返回）。
func (h *Hub) stopPump() {
	h.pumpMu.Lock()
	cancel := h.pumpCancel
	h.pumpMu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.pumpWG.Wait()
}

// pumpLoop 是轮询泵主循环：启动时取一次全局水位（<=0 = 从现在开始，绝不重放历史），
// 之后每 PollInterval 一轮；**没有 WS 连接时不查库**。
func (h *Hub) pumpLoop(ctx context.Context) {
	interval := h.cfg.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	h.initWatermarks(ctx)
	h.log.Info("WS 轮询泵启动", "interval", interval.String(), "ring_cap", ringCap)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.log.Info("WS 轮询泵退出")
			return
		case <-t.C:
		}
		if h.wsCount() == 0 {
			continue
		}
		h.pumpTick(ctx)
	}
}

// initWatermarks 取两个表的全局水位（对齐 sse_run 的「<=0 从现在开始」）。
func (h *Hub) initWatermarks(ctx context.Context) {
	pm, err := h.src.maxID(ctx, "social_pm_messages")
	if err != nil {
		h.log.Warn("pm 全局水位初始化失败, 稍后重试", "err", err)
	} else {
		h.evMu.Lock()
		h.pmWater = pm
		h.evMu.Unlock()
	}
	grp, err := h.src.maxID(ctx, "social_messages")
	if err != nil {
		h.log.Warn("group 全局水位初始化失败, 稍后重试", "err", err)
	} else {
		h.evMu.Lock()
		h.grpWater = grp
		h.evMu.Unlock()
	}
}

// pumpTick 是一轮：先拉新行进环缓并推进全局水位，再按连接游标投递。
func (h *Hub) pumpTick(ctx context.Context) {
	h.pollNewPM(ctx)
	if ctx.Err() != nil {
		return
	}
	h.pollNewGroup(ctx)
	if ctx.Err() != nil {
		return
	}
	h.deliverOnce(ctx, time.Now())
}

// pollNewPM 拉取全局新私信（列/排序/LIMIT 与 PHP 一致），推进全局水位。
func (h *Hub) pollNewPM(ctx context.Context) {
	h.evMu.Lock()
	since := h.pmWater
	h.evMu.Unlock()
	if since <= 0 {
		// 启动时初始化失败（例如 DB 刚起）就在这里补一次；失败则跳过本轮，
		// 绝不从 0 开始「从头往回捞」。
		max, err := h.src.maxID(ctx, "social_pm_messages")
		if err != nil {
			h.log.Warn("pm 全局水位初始化失败", "err", err)
			return
		}
		since = max
		h.evMu.Lock()
		if max > h.pmWater {
			h.pmWater = max
		}
		h.evMu.Unlock()
	}

	rows, err := h.src.newPM(ctx, since)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("pm 全局轮询失败", "err", err)
		}
		return
	}
	var last int64
	for _, row := range rows {
		if row.id > last {
			last = row.id
		}
	}
	h.evMu.Lock()
	for _, row := range rows {
		h.pmRing.push(row.id, row)
	}
	if last > h.pmWater {
		h.pmWater = last
	}
	h.evMu.Unlock()
}

// pollNewGroup 拉取全局新群消息（列/排序/LIMIT 与 PHP 一致），推进全局水位。
func (h *Hub) pollNewGroup(ctx context.Context) {
	h.evMu.Lock()
	since := h.grpWater
	h.evMu.Unlock()
	if since <= 0 {
		max, err := h.src.maxID(ctx, "social_messages")
		if err != nil {
			h.log.Warn("group 全局水位初始化失败", "err", err)
			return
		}
		since = max
		h.evMu.Lock()
		if max > h.grpWater {
			h.grpWater = max
		}
		h.evMu.Unlock()
	}

	rows, err := h.src.newGroup(ctx, since)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("group 全局轮询失败", "err", err)
		}
		return
	}
	var last int64
	for _, row := range rows {
		if row.id > last {
			last = row.id
		}
	}
	h.evMu.Lock()
	for _, row := range rows {
		h.grpRing.push(row.id, row)
	}
	if last > h.grpWater {
		h.grpWater = last
	}
	h.evMu.Unlock()
}

// deliverOnce 把环缓里 id > 各连接游标的事件按 id 升序投递给所有 WS 连接，
// 并**无条件把该连接的游标推进到全局水位**（对齐 PHP「先推游标再跳过自己发的」：
// 否则自己发消息时会把别人的事件漏掉）。投递全走 dispatch（非阻塞，绝不拖住轮询）。
func (h *Hub) deliverOnce(ctx context.Context, now time.Time) {
	h.mu.Lock()
	ws := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		if c.kind == "ws" {
			ws = append(ws, c)
		}
	}
	h.mu.Unlock()
	if len(ws) == 0 {
		return
	}

	h.evMu.Lock()
	pmWater, grpWater := h.pmWater, h.grpWater
	pmRing, grpRing := h.pmRing, h.grpRing
	h.evMu.Unlock()

	for _, c := range ws {
		if cur := c.pmCur.Load(); pmWater > cur {
			for _, e := range pmRing.since(cur) {
				row, ok := e.row.(pmRow)
				if !ok {
					continue
				}
				if ev, ok := row.renderPM(c.userID); ok {
					h.dispatch(c, wsEventFrame(ev))
				}
			}
			c.pmCur.Store(pmWater)
		}
		if cur := c.grpCur.Load(); grpWater > cur {
			muted := h.mutedFor(ctx, c, now)
			for _, e := range grpRing.since(cur) {
				row, ok := e.row.(grpRow)
				if !ok {
					continue
				}
				if ev, ok := row.renderGroup(c.userID, muted); ok {
					h.dispatch(c, wsEventFrame(ev))
				}
			}
			c.grpCur.Store(grpWater)
		}
	}
}

// mutedFor 取某条 WS 连接用户的免打扰群集合，10 秒内复用缓存
// （对齐 PHP：$mutedIds 在心跳时刷新，免打扰变化 10 秒内生效）。
func (h *Hub) mutedFor(ctx context.Context, c *conn, now time.Time) map[int64]bool {
	c.mutedMu.Lock()
	defer c.mutedMu.Unlock()
	if c.muted == nil || now.Sub(c.mutedAt) >= mutedTTL {
		if h.db == nil {
			c.muted = map[int64]bool{}
		} else {
			c.muted = h.db.MutedGroupIDs(ctx, c.userID)
		}
		c.mutedAt = now
	}
	return c.muted
}
