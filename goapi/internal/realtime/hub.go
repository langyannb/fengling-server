// Package realtime 实现原生 SSE 与 WebSocket 之间的公共部分：
// 连接注册表（内存 hub）、Redis 在线状态、以及**事件来源**。
//
// 阶段 0 的事件来源是与 PHP 完全相同的 1 秒 DB 轮询（pollOnce），
// 刻意抽成一个函数 + 一个 emit 回调：阶段 3/4 换成 Redis Pub/Sub 时，
// 只要把 pollOnce 换成订阅 fls:events 并把消息喂给同一个 emit 即可，SSE/WS 侧不用改。
package realtime

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

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

	closed chan struct{}
	once   sync.Once
}

// Hub 是内存连接注册表 + 事件轮询器。
type Hub struct {
	cfg config.Config
	db  *store.Store
	rd  *store.Redis
	log *slog.Logger

	mu    sync.Mutex
	conns map[*conn]struct{}
	seq   atomic.Uint64
}

func newHub(cfg config.Config, db *store.Store, rd *store.Redis, log *slog.Logger) *Hub {
	return &Hub{cfg: cfg, db: db, rd: rd, log: log, conns: make(map[*conn]struct{})}
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
	h.log.Info("实时连接建立", "conn", c.id, "uid", userID, "kind", kind, "active", n)
	return c
}

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
	c.once.Do(func() { close(c.closed) })
}

func (h *Hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// closeAll 进程退出时用。
func (h *Hub) closeAll() {
	h.mu.Lock()
	all := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		all = append(all, c)
	}
	h.mu.Unlock()
	for _, c := range all {
		h.remove(c)
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

// ---------- 事件来源：1 秒轮询（与 PHP sse_run 完全同构） ----------

const (
	pmLimit  = 20
	grpLimit = 30
	// vselPM / vselGrp 与 API 里 db_has_column('…','video') 为真时拼上去的片段一字不差。
	vselVideo = ", m.video, m.video_w, m.video_h, m.video_duration, m.video_size"
)

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

// pollPM 对齐 api.php:3698-3727。
func (h *Hub) pollPM(ctx context.Context, c *conn, emit func(event) bool) {
	useVideo := h.db.HasColumn(ctx, "social_pm_messages", "video")
	vsel := ""
	if useVideo {
		vsel = vselVideo
	}
	query := `SELECT m.id, m.conv_id, m.from_user, m.content, m.image` + vsel + `, m.created_at,
	                 u.nickname, u.username
	            FROM social_pm_messages m JOIN users u ON u.id = m.from_user
	           WHERE m.to_user = ? AND m.id > ? AND m.is_recalled = 0
	           ORDER BY m.id ASC LIMIT 20`
	cur := c.pmCur.Load()
	rows, err := h.db.DB().QueryContext(ctx, query, c.userID, cur)
	if err != nil {
		h.log.Warn("pm 轮询失败", "conn", c.id, "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, convID, fromUser                int64
			content, image, createdAt           sql.NullString
			nickname, username                  sql.NullString
			video                               sql.NullString
			videoW, videoH, videoDur, videoSize sql.NullInt64
		)
		if useVideo {
			err = rows.Scan(&id, &convID, &fromUser, &content, &image,
				&video, &videoW, &videoH, &videoDur, &videoSize,
				&createdAt, &nickname, &username)
		} else {
			err = rows.Scan(&id, &convID, &fromUser, &content, &image,
				&createdAt, &nickname, &username)
		}
		if err != nil {
			h.log.Warn("pm 行解析失败", "conn", c.id, "err", err)
			return
		}
		// 先推进游标，再判断是不是自己发的（与 PHP 顺序一致）
		if id > cur {
			cur = id
			c.pmCur.Store(id)
		}
		if fromUser == c.userID {
			continue
		}
		nick := nickname.String
		if nick == "" {
			nick = "用户" + strconv.FormatInt(fromUser, 10)
		}
		msgType := ""
		if video.String != "" {
			msgType = "video"
		}
		d := phpjson.New().
			Set("id", id).
			Set("conv_id", convID).
			Set("from_user", fromUser).
			Set("nickname", nick).
			Set("username", username.String).
			Set("content", content.String).
			Set("image", image.String).
			Set("msg_type", msgType).
			Set("video", video.String).
			Set("video_w", videoW.Int64).
			Set("video_h", videoH.Int64).
			Set("video_duration", videoDur.Int64).
			Set("video_size", videoSize.Int64).
			Set("created_at", createdAt.String)
		if !emit(event{name: "pm", data: d}) {
			return
		}
	}
	if err := rows.Err(); err != nil {
		h.log.Warn("pm 轮询中断", "conn", c.id, "err", err)
	}
}

// pollGroup 对齐 api.php:3729-3772。
func (h *Hub) pollGroup(ctx context.Context, c *conn, muted map[int64]bool, emit func(event) bool) {
	useVideo := h.db.HasColumn(ctx, "social_messages", "video")
	vsel := ""
	if useVideo {
		vsel = vselVideo
	}
	query := `SELECT m.id, m.group_id, m.user_id, m.content, m.image, m.at_users, m.msg_type` + vsel + `, m.created_at,
	                 u.nickname, g.name AS group_name
	            FROM social_messages m
	            JOIN users u ON u.id = m.user_id
	            LEFT JOIN social_groups g ON g.id = m.group_id
	           WHERE m.id > ? AND m.user_id <> ? AND m.is_recalled = 0
	           ORDER BY m.id ASC LIMIT 30`
	cur := c.grpCur.Load()
	rows, err := h.db.DB().QueryContext(ctx, query, cur, c.userID)
	if err != nil {
		h.log.Warn("group 轮询失败", "conn", c.id, "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, groupID, userID                 int64
			content, image, atUsers, msgType    sql.NullString
			video                               sql.NullString
			videoW, videoH, videoDur, videoSize sql.NullInt64
			createdAt, nickname, groupName      sql.NullString
		)
		if useVideo {
			err = rows.Scan(&id, &groupID, &userID, &content, &image, &atUsers, &msgType,
				&video, &videoW, &videoH, &videoDur, &videoSize,
				&createdAt, &nickname, &groupName)
		} else {
			err = rows.Scan(&id, &groupID, &userID, &content, &image, &atUsers, &msgType,
				&createdAt, &nickname, &groupName)
		}
		if err != nil {
			h.log.Warn("group 行解析失败", "conn", c.id, "err", err)
			return
		}
		if id > cur {
			cur = id
			c.grpCur.Store(id)
		}

		atMe, atAll := 0, 0
		// 逐字复刻 PHP：`$v = (int)trim($v); if ($v === 0) $atAll = 1;`
		// 所以空串或非数字段（例如 at_users 结尾多了个逗号）都会把 at_all 置 1 —— 这是
		// 客户端已经适配的既有行为，必须照抄，不能「顺手修好」。
		if raw := atUsers.String; raw != "" {
			for _, seg := range strings.Split(raw, ",") {
				v := phpAtoi(strings.TrimSpace(seg))
				if v == 0 {
					atAll = 1
				}
				if v > 0 && v == c.userID {
					atMe = 1
				}
			}
		}
		nick := nickname.String
		if nick == "" {
			nick = "用户" + strconv.FormatInt(userID, 10)
		}
		mutedFlag := 0
		if muted[groupID] {
			mutedFlag = 1
		}
		d := phpjson.New().
			Set("id", id).
			Set("group_id", groupID).
			Set("group_name", groupName.String).
			Set("user_id", userID).
			Set("nickname", nick).
			Set("content", content.String).
			Set("image", image.String).
			Set("video", video.String).
			Set("video_w", videoW.Int64).
			Set("video_h", videoH.Int64).
			Set("video_duration", videoDur.Int64).
			Set("video_size", videoSize.Int64).
			Set("at_me", atMe).
			Set("at_all", atAll).
			Set("muted", mutedFlag).
			Set("msg_type", msgType.String).
			Set("created_at", createdAt.String)
		if !emit(event{name: "group", data: d}) {
			return
		}
	}
	if err := rows.Err(); err != nil {
		h.log.Warn("group 轮询中断", "conn", c.id, "err", err)
	}
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
