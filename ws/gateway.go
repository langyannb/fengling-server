package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Gateway forwards send frames to the existing PHP action so bans, group
// membership, @ handling, notifications and last_message stay in one place.
// The Go service owns transport, ACK, read/typing and online fanout.
type Gateway struct {
	base   string
	client *http.Client
}

func NewGateway(base string) *Gateway {
	return &Gateway{
		base:   base,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (g *Gateway) call(ctx context.Context, token, action string, body any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+"?action="+action, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("PHP 响应不是 JSON: %w", err)
	}
	if out.Code != 0 {
		if out.Msg == "" {
			out.Msg = "发送失败"
		}
		return nil, &apiError{Code: out.Code, Msg: out.Msg}
	}
	var result map[string]any
	if len(out.Data) > 0 {
		_ = json.Unmarshal(out.Data, &result)
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

func (g *Gateway) SendPM(ctx context.Context, token string, s SendData) (map[string]any, error) {
	body := map[string]any{
		"client_msg_id":  s.ClientMsgID,
		"to_user":        s.ToUser,
		"conv_id":        s.ConvID,
		"content":        s.Content,
		"image":          s.Image,
		"image_w":        s.ImageW,
		"image_h":        s.ImageH,
		"video":          s.Video,
		"video_w":        s.VideoW,
		"video_h":        s.VideoH,
		"video_duration": s.VideoDur,
		"video_size":     s.VideoSize,
		"msg_type":       s.MsgType,
	}
	return g.call(ctx, token, "pm_send", body)
}

func (g *Gateway) SendGroup(ctx context.Context, token string, s SendData) (map[string]any, error) {
	body := map[string]any{
		"client_msg_id":  s.ClientMsgID,
		"group_id":       s.GroupID,
		"content":        s.Content,
		"image":          s.Image,
		"image_w":        s.ImageW,
		"image_h":        s.ImageH,
		"video":          s.Video,
		"video_w":        s.VideoW,
		"video_h":        s.VideoH,
		"video_duration": s.VideoDur,
		"video_size":     s.VideoSize,
		"msg_type":       s.MsgType,
		"at":             s.At,
		"at_all":         s.AtAll,
		"quote_id":       s.QuoteID,
	}
	return g.call(ctx, token, "social_send", body)
}

type apiError struct {
	Code int
	Msg  string
}

func (e *apiError) Error() string { return e.Msg }

func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		var i int
		_, _ = fmt.Sscanf(n, "%d", &i)
		return i
	}
	return 0
}

func int64From(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		var i int64
		_, _ = fmt.Sscanf(n, "%d", &i)
		return i
	}
	return 0
}

func stringFrom(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
