package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.CreateSession(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, "s1"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create: got %v, want ErrExists", err)
	}
	if err := s.SetSessionAccount(ctx, "s1", "123@s.whatsapp.net", "Me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionStatus(ctx, "s1", "ready"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.JID != "123@s.whatsapp.net" || got.PushName != "Me" || got.Status != "ready" {
		t.Fatalf("unexpected session: %+v", got)
	}
	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: got %v", err)
	}
}

func TestSaveMessageUpdatesChat(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	chat := "62811@s.whatsapp.net"

	save := func(m Message, live bool) bool {
		t.Helper()
		ok, err := s.SaveMessage(ctx, "s1", m, live)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	save(Message{ID: "a", ChatID: chat, Timestamp: 100, Type: "text", Text: "first", Status: StatusReceived}, true)
	save(Message{ID: "b", ChatID: chat, Timestamp: 200, Type: "text", Text: "second", Status: StatusReceived}, true)
	// An older message arriving late (e.g. from history sync) must not replace the preview.
	save(Message{ID: "old", ChatID: chat, Timestamp: 50, Type: "text", Text: "older"}, false)
	if save(Message{ID: "b", ChatID: chat, Timestamp: 200, Type: "text", Text: "dup"}, true) {
		t.Fatal("duplicate message reported as inserted")
	}

	chats, err := s.ListChats(ctx, "s1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 {
		t.Fatalf("got %d chats", len(chats))
	}
	c := chats[0]
	if c.LastMessage == nil || c.LastMessage.ID != "b" || c.LastMessage.Text != "second" || c.LastMessage.Timestamp != 200 {
		t.Fatalf("unexpected last message: %+v", c.LastMessage)
	}
	if c.Unread != 2 {
		t.Fatalf("unread = %d, want 2 (live incoming only)", c.Unread)
	}

	msgs, err := s.ListMessages(ctx, "s1", chat, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[0].ID != "b" || msgs[2].ID != "old" {
		t.Fatalf("unexpected order: %+v", msgs)
	}
	page, _ := s.ListMessages(ctx, "s1", chat, 200, 10)
	if len(page) != 2 || page[0].ID != "a" {
		t.Fatalf("before=200 page: %+v", page)
	}

	if err := s.ResetUnread(ctx, "s1", chat); err != nil {
		t.Fatal(err)
	}
	chats, _ = s.ListChats(ctx, "s1", 10, 0)
	if chats[0].Unread != 0 {
		t.Fatal("unread not reset")
	}
}

func TestStatusOnlyMovesForward(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	chat := "62811@s.whatsapp.net"
	_, _ = s.SaveMessage(ctx, "s1", Message{ID: "m", ChatID: chat, FromMe: true, Timestamp: 1, Type: "text", Status: StatusSent}, false)
	_, _ = s.SaveMessage(ctx, "s1", Message{ID: "in", ChatID: chat, Timestamp: 2, Type: "text", Status: StatusReceived}, false)

	changed, err := s.UpdateStatus(ctx, "s1", chat, []string{"m", "in"}, StatusRead)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "m" {
		t.Fatalf("changed = %v, want [m] (incoming messages are not updated)", changed)
	}
	changed, _ = s.UpdateStatus(ctx, "s1", chat, []string{"m"}, StatusDelivered)
	if len(changed) != 0 {
		t.Fatal("status moved backwards")
	}
	m, _ := s.GetMessage(ctx, "s1", chat, "m")
	if m.Status != StatusRead {
		t.Fatalf("status = %q", m.Status)
	}
}

func TestEditAndRevoke(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	chat := "123@g.us"
	_, _ = s.SaveMessage(ctx, "s1", Message{ID: "m", ChatID: chat, Timestamp: 1, Type: "text", Text: "helo"}, false)

	if ok, err := s.EditMessage(ctx, "s1", chat, "m", "hello"); !ok || err != nil {
		t.Fatalf("edit: %v %v", ok, err)
	}
	m, _ := s.GetMessage(ctx, "s1", chat, "m")
	if m.Text != "hello" || !m.Edited || m.Type != "text" {
		t.Fatalf("after edit: %+v", m)
	}
	chats, _ := s.ListChats(ctx, "s1", 10, 0)
	if chats[0].LastMessage.Text != "hello" || !chats[0].IsGroup {
		t.Fatalf("chat after edit: %+v", chats[0])
	}

	if ok, _ := s.RevokeMessage(ctx, "s1", chat, "m"); !ok {
		t.Fatal("revoke not found")
	}
	m, _ = s.GetMessage(ctx, "s1", chat, "m")
	if m.Type != TypeRevoked || m.Text != "" {
		t.Fatalf("after revoke: %+v", m)
	}
	if ok, _ := s.EditMessage(ctx, "s1", chat, "missing", "x"); ok {
		t.Fatal("edit of missing message reported found")
	}
}

func TestUpsertChatAndPurge(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	chat := "62811@s.whatsapp.net"
	unread := 3
	if err := s.UpsertChat(ctx, "s1", chat, "Alice", &unread); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChat(ctx, "s1", chat, "", nil); err != nil { // must not clear name or unread
		t.Fatal(err)
	}
	if got := s.ChatName(ctx, "s1", chat); got != "Alice" {
		t.Fatalf("name = %q", got)
	}
	chats, _ := s.ListChats(ctx, "s1", 10, 0)
	if chats[0].Unread != 3 || chats[0].LastMessage != nil {
		t.Fatalf("chat: %+v", chats[0])
	}

	_, _ = s.SaveMessage(ctx, "s1", Message{ID: "m", ChatID: chat, Timestamp: 1, Type: "text"}, false)
	if err := s.PurgeSessionData(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	chats, _ = s.ListChats(ctx, "s1", 10, 0)
	msgs, _ := s.ListMessages(ctx, "s1", chat, 0, 10)
	if len(chats) != 0 || len(msgs) != 0 {
		t.Fatal("purge left data behind")
	}
	if _, err := s.GetSession(ctx, "s1"); err != nil {
		t.Fatal("purge removed the session row")
	}
}
