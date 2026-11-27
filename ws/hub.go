package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 30 * time.Second
	pollPeriod     = 500 * time.Millisecond
	heartbeatSec   = 15
	clientQueueCap = 256
)

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	userID int
	token  string

	send chan []byte
	done chan struct{}

	pmCursor    int64
	groupCursor int64
	closeOnce   sync.Once
}

func (c *Client) enqueue(payload []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- payload:
	default:
		// Slow client: drop it instead of letting the poller block.
		c.close()
	}
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
		c.hub.remove(c)
	})
}

type Hub struct {
	store *Store
	gw    *Gateway

	mu      sync.RWMutex
	clients map[*Client]struct{}
	byUser  map[int]map[*Client]struct{}
}

func NewHub(store *Store, gw *Gateway) *Hub {
	return &Hub{
		store:   store,
		gw:      gw,
		clients: map[*Client]struct{}{},
		byUser:  map[int]map[*Client]struct{}{},
	}
}

func (h *Hub) add(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	if h.byUser[c.userID] == nil {
		h.byUser[c.userID] = map[*Client]struct{}{}
	}
	h.byUser[c.userID][c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	if set := h.byUser[c.userID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.byUser, c.userID)
		}
	}
	h.mu.Unlock()
}

func (h *Hub) snapshot() []*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		out = append(out, c)
	}
	return out
}

func (h *Hub) userClients(userID int) []*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Client, 0, len(h.byUser[userID]))
	for c := range h.byUser[userID] {
		out = append(out, c)
	}
	return out
}

func (h *Hub) sendUser(userID int, payload []byte, except *Client) {
	for _, c := range h.userClients(userID) {
		if c != except {
			c.enqueue(payload)
		}
	}
}

func (h *Hub) sendGroup(groupID int, payload []byte, exceptUser int) {
	members, err := h.store.GroupMembers(context.Background(), groupID)
	if err != nil {
		log.Printf("group members %d: %v", groupID, err)
		return
	}
	h.mu.RLock()
	clients := make([]*Client, 0)
	for c := range h.clients {
		if c.userID != exceptUser && members[c.userID] {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.enqueue(payload)
	}
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	userID, err := h.store.UserID(ctx, token)
	if err != nil {
		http.Error(w, `{"type":"error","code":401,"msg":"登录已失效"}`, http.StatusUnauthorized)
		return
	}
	pmCursor := parseInt64(r.URL.Query().Get("pm_id"))
	groupCursor := parseInt64(r.URL.Query().Get("group_id"))
	if pmCursor <= 0 {
		pmCursor, _ = h.store.PmMaxID(ctx)
	}
	if groupCursor <= 0 {
		groupCursor, _ = h.store.GroupMaxID(ctx)
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	c := &Client{
		hub:         h,
		conn:        conn,
		userID:      userID,
		token:       token,
		send:        make(chan []byte, clientQueueCap),
		done:        make(chan struct{}),
		pmCursor:    pmCursor,
		groupCursor: groupCursor,
	}
	h.add(c)
	c.enqueue(frame("hello", HelloData{
		UserID:       userID,
		ServerTime:   nowUnix(),
		HeartbeatSec: heartbeatSec,
		Cursor:       Cursor{PmID: pmCursor, GroupID: groupCursor},
		Features:     []string{"send_ack", "read", "typing"},
	}))
	go c.writePump()
	c.readPump()
}

func (h *Hub) handleFrame(c *Client, raw []byte) {
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		c.enqueue(errorFrame(400, "帧不是 JSON"))
		return
	}
	switch f.Type {
	case "send":
		var s SendData
		if err := json.Unmarshal(f.Data, &s); err != nil {
			c.enqueue(errorFrame(400, "send 参数错误"))
			return
		}
		if err := s.validate(); err != nil {
			c.enqueue(errorFrame(400, err.Error()))
			return
		}
		go h.forwardSend(c, s)
	case "read":
		var d ReadData
		if err := json.Unmarshal(f.Data, &d); err != nil {
			c.enqueue(errorFrame(400, "read 参数错误"))
			return
		}
		go h.handleRead(c, d)
	case "typing":
		var d TypingData
		if err := json.Unmarshal(f.Data, &d); err != nil {
			return
		}
		h.handleTyping(c, d)
	case "ping":
		c.enqueue(frame("pong", map[string]any{"t": nowUnix()}))
	}
}

func (h *Hub) forwardSend(c *Client, s SendData) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var res map[string]any
	var err error
	if s.GroupID > 0 {
		res, err = h.gw.SendGroup(ctx, c.token, s)
	} else {
		res, err = h.gw.SendPM(ctx, c.token, s)
	}
	if err != nil {
		code := 400
		if e, ok := err.(*apiError); ok {
			code = e.Code
		}
		c.enqueue(errorFrame(code, err.Error()))
		return
	}
	ack := AckData{
		ClientMsgID: s.ClientMsgID,
		ID:          int64From(res["id"]),
		ConvID:      intFrom(res["conv_id"]),
		GroupID:     intFrom(res["group_id"]),
		CreatedAt:   stringFrom(res["created_at"]),
	}
	if s.GroupID > 0 {
		ack.GroupID = s.GroupID
	}
	c.enqueue(frame("ack", ack))
}

func (h *Hub) handleRead(c *Client, d ReadData) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev := ReadEvent{UserID: c.userID, LastReadID: d.LastReadID}
	if d.GroupID > 0 {
		if err := h.store.SetGroupRead(ctx, c.userID, d.GroupID, d.LastReadID); err != nil {
			c.enqueue(errorFrame(500, "已读更新失败"))
			return
		}
		ev.GroupID = d.GroupID
		h.sendGroup(d.GroupID, frame("read", ev), c.userID)
		return
	}
	if d.ConvID <= 0 {
		return
	}
	if err := h.store.SetPmRead(ctx, c.userID, d.ConvID, d.LastReadID); err != nil {
		c.enqueue(errorFrame(500, "已读更新失败"))
		return
	}
	ev.ConvID = d.ConvID
	peer, err := h.store.PmPeer(ctx, d.ConvID, c.userID)
	if err == nil {
		h.sendUser(peer, frame("read", ev), c)
	}
}

func (h *Hub) handleTyping(c *Client, d TypingData) {
	ev := TypingEvent{UserID: c.userID}
	if d.GroupID > 0 {
		ev.GroupID = d.GroupID
		h.sendGroup(d.GroupID, frame("typing", ev), c.userID)
		return
	}
	if d.ConvID <= 0 {
		return
	}
	ev.ConvID = d.ConvID
	peer, err := h.store.PmPeer(context.Background(), d.ConvID, c.userID)
	if err == nil {
		h.sendUser(peer, frame("typing", ev), c)
	}
}

func (h *Hub) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(pollPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.pollOnce(ctx)
		}
	}
}

func (h *Hub) pollOnce(ctx context.Context) {
	clients := h.snapshot()
	if len(clients) == 0 {
		return
	}
	minPm, minGroup := int64(-1), int64(-1)
	for _, c := range clients {
		if minPm < 0 || c.pmCursor < minPm {
			minPm = c.pmCursor
		}
		if minGroup < 0 || c.groupCursor < minGroup {
			minGroup = c.groupCursor
		}
	}
	if minPm >= 0 {
		h.pollPM(ctx, clients, minPm)
	}
	if minGroup >= 0 {
		h.pollGroup(ctx, clients, minGroup)
	}
}

func (h *Hub) pollPM(ctx context.Context, clients []*Client, floor int64) {
	rows, err := h.store.PmAfter(ctx, floor, 500)
	if err != nil {
		log.Printf("poll pm: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	maxID := floor
	for _, m := range rows {
		if m.ID > maxID {
			maxID = m.ID
		}
		payload := frame("pm", m)
		for _, c := range clients {
			if c.userID == m.ToUser && c.pmCursor < m.ID {
				c.enqueue(payload)
			}
		}
	}
	for _, c := range clients {
		if c.pmCursor < maxID {
			c.pmCursor = maxID
		}
	}
}

func (h *Hub) pollGroup(ctx context.Context, clients []*Client, floor int64) {
	rows, err := h.store.GroupAfter(ctx, floor, 500)
	if err != nil {
		log.Printf("poll group: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	maxID := floor
	for _, m := range rows {
		if m.ID > maxID {
			maxID = m.ID
		}
		members, err := h.store.GroupMembers(ctx, m.GroupID)
		if err != nil {
			continue
		}
		for _, c := range clients {
			if c.groupCursor >= m.ID || !members[c.userID] {
				continue
			}
			out := m
			out.AtMe, out.AtAll = atFor(out.AtUsers, c.userID)
			if h.store.IsMuted(ctx, c.userID, m.GroupID) {
				out.Muted = 1
			}
			c.enqueue(frame("group", out))
		}
	}
	for _, c := range clients {
		if c.groupCursor < maxID {
			c.groupCursor = maxID
		}
	}
}

func atFor(raw string, userID int) (int, int) {
	atMe, atAll := 0, 0
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "0" {
			atAll = 1
		} else if p != "" {
			if n, err := strconv.Atoi(p); err == nil && n == userID {
				atMe = 1
			}
		}
	}
	return atMe, atAll
}

func (c *Client) readPump() {
	defer c.close()
	c.conn.SetReadLimit(1 << 20)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.hub.handleFrame(c, raw)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()
	for {
		select {
		case payload := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-c.done:
			return
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
