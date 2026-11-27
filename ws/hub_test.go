package main

import "testing"

func testClient(h *Hub, userID int) *Client {
	c := &Client{
		hub:    h,
		userID: userID,
		send:   make(chan []byte, 4),
		done:   make(chan struct{}),
	}
	if h.byUser[userID] == nil {
		h.byUser[userID] = map[*Client]struct{}{}
	}
	h.byUser[userID][c] = struct{}{}
	h.clients[c] = struct{}{}
	return c
}

func TestSendUser(t *testing.T) {
	h := NewHub(nil, nil)
	a := testClient(h, 1)
	b := testClient(h, 1)
	other := testClient(h, 2)

	h.sendUser(1, []byte("hello"), nil)
	for _, c := range []*Client{a, b} {
		select {
		case got := <-c.send:
			if string(got) != "hello" {
				t.Fatalf("got %q", got)
			}
		default:
			t.Fatal("expected message")
		}
	}
	select {
	case <-other.send:
		t.Fatal("other user should not receive")
	default:
	}
}

func TestSendUserExcept(t *testing.T) {
	h := NewHub(nil, nil)
	a := testClient(h, 1)
	b := testClient(h, 1)
	h.sendUser(1, []byte("x"), a)
	select {
	case <-a.send:
		t.Fatal("sender should be excluded")
	default:
	}
	select {
	case <-b.send:
	default:
		t.Fatal("other connection should receive")
	}
}
