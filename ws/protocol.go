package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// Frame is the only envelope on the wire. Data stays raw so a malformed
// frame can be rejected without failing the connection.
type Frame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	Code int             `json:"code,omitempty"`
	Msg  string          `json:"msg,omitempty"`
}

func frame(typ string, data any) []byte {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	out, _ := json.Marshal(Frame{Type: typ, Data: raw})
	return out
}

func errorFrame(code int, msg string) []byte {
	out, _ := json.Marshal(Frame{Type: "error", Code: code, Msg: msg})
	return out
}

type Cursor struct {
	PmID    int64 `json:"pm_id"`
	GroupID int64 `json:"group_id"`
}

type HelloData struct {
	UserID       int      `json:"user_id"`
	ServerTime   int64    `json:"server_time"`
	HeartbeatSec int      `json:"heartbeat_sec"`
	Cursor       Cursor   `json:"cursor"`
	Features     []string `json:"features,omitempty"`
}

type SendData struct {
	ClientMsgID string `json:"client_msg_id"`
	ToUser      int    `json:"to_user"`
	ConvID      int    `json:"conv_id"`
	GroupID     int    `json:"group_id"`
	Content     string `json:"content"`
	Image       string `json:"image"`
	ImageW      int    `json:"image_w"`
	ImageH      int    `json:"image_h"`
	Video       string `json:"video"`
	VideoW      int    `json:"video_w"`
	VideoH      int    `json:"video_h"`
	VideoDur    int    `json:"video_duration"`
	VideoSize   int64  `json:"video_size"`
	MsgType     string `json:"msg_type"`
	At          []int  `json:"at"`
	AtAll       int    `json:"at_all"`
	QuoteID     int    `json:"quote_id"`
}

type AckData struct {
	ClientMsgID string `json:"client_msg_id"`
	ID          int64  `json:"id"`
	ConvID      int    `json:"conv_id,omitempty"`
	GroupID     int    `json:"group_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type ReadData struct {
	ConvID     int   `json:"conv_id"`
	GroupID    int   `json:"group_id"`
	LastReadID int64 `json:"last_read_id"`
}

type TypingData struct {
	ConvID  int `json:"conv_id"`
	GroupID int `json:"group_id"`
}

type ReadEvent struct {
	ConvID     int   `json:"conv_id,omitempty"`
	GroupID    int   `json:"group_id,omitempty"`
	UserID     int   `json:"user_id"`
	LastReadID int64 `json:"last_read_id"`
}

type TypingEvent struct {
	ConvID  int `json:"conv_id,omitempty"`
	GroupID int `json:"group_id,omitempty"`
	UserID  int `json:"user_id"`
}

type PmMsg struct {
	ID            int64  `json:"id"`
	ConvID        int    `json:"conv_id"`
	FromUser      int    `json:"from_user"`
	ToUser        int    `json:"-"`
	Nickname      string `json:"nickname"`
	Username      string `json:"username,omitempty"`
	Content       string `json:"content"`
	Image         string `json:"image"`
	ImageW        int    `json:"image_w"`
	ImageH        int    `json:"image_h"`
	Video         string `json:"video"`
	VideoW        int    `json:"video_w"`
	VideoH        int    `json:"video_h"`
	VideoDuration int    `json:"video_duration"`
	VideoSize     int64  `json:"video_size"`
	MsgType       string `json:"msg_type"`
	CreatedAt     string `json:"created_at"`
	ClientMsgID   string `json:"client_msg_id,omitempty"`
}

type GroupMsg struct {
	ID            int64  `json:"id"`
	GroupID       int    `json:"group_id"`
	GroupName     string `json:"group_name"`
	UserID        int    `json:"user_id"`
	Nickname      string `json:"nickname"`
	Username      string `json:"username,omitempty"`
	Content       string `json:"content"`
	Image         string `json:"image"`
	ImageW        int    `json:"image_w"`
	ImageH        int    `json:"image_h"`
	Video         string `json:"video"`
	VideoW        int    `json:"video_w"`
	VideoH        int    `json:"video_h"`
	VideoDuration int    `json:"video_duration"`
	VideoSize     int64  `json:"video_size"`
	AtMe          int    `json:"at_me"`
	AtAll         int    `json:"at_all"`
	Muted         int    `json:"muted"`
	MsgType       string `json:"msg_type"`
	CreatedAt     string `json:"created_at"`
	AtUsers       string `json:"-"`
}

func (s SendData) validate() error {
	if s.ClientMsgID == "" {
		return fmt.Errorf("缺少 client_msg_id")
	}
	if len(s.ClientMsgID) > 64 {
		return fmt.Errorf("client_msg_id 过长")
	}
	if s.GroupID <= 0 && s.ToUser <= 0 && s.ConvID <= 0 {
		return fmt.Errorf("缺少收件人")
	}
	if s.Content == "" && s.Image == "" && s.Video == "" {
		return fmt.Errorf("消息内容不能为空")
	}
	return nil
}

func nowUnix() int64 { return time.Now().Unix() }
