package main

import (
	"encoding/json"
	"testing"
)

func TestSendDataValidate(t *testing.T) {
	cases := []struct {
		name string
		in   SendData
		ok   bool
	}{
		{"valid pm", SendData{ClientMsgID: "a", ToUser: 2, Content: "hi"}, true},
		{"valid group", SendData{ClientMsgID: "a", GroupID: 3, Content: "hi"}, true},
		{"valid conv", SendData{ClientMsgID: "a", ConvID: 4, Content: "hi"}, true},
		{"missing client id", SendData{ToUser: 2, Content: "hi"}, false},
		{"missing target", SendData{ClientMsgID: "a", Content: "hi"}, false},
		{"empty body", SendData{ClientMsgID: "a", ToUser: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.validate()
			if (err == nil) != tc.ok {
				t.Fatalf("validate() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestAtFor(t *testing.T) {
	atMe, atAll := atFor("0,7,9", 7)
	if atMe != 1 || atAll != 1 {
		t.Fatalf("at=7 => %d,%d want 1,1", atMe, atAll)
	}
	atMe, atAll = atFor("9", 7)
	if atMe != 0 || atAll != 0 {
		t.Fatalf("at=7 => %d,%d want 0,0", atMe, atAll)
	}
}

func TestFrame(t *testing.T) {
	raw := frame("ack", AckData{ClientMsgID: "x", ID: 1})
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Type != "ack" {
		t.Fatalf("type=%s", f.Type)
	}
	var a AckData
	if err := json.Unmarshal(f.Data, &a); err != nil {
		t.Fatal(err)
	}
	if a.ClientMsgID != "x" || a.ID != 1 {
		t.Fatalf("ack=%+v", a)
	}
}
