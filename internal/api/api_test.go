package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rockcacing/mywa/internal/hub"
	"github.com/rockcacing/mywa/internal/store"
	"github.com/rockcacing/mywa/internal/wa"
)

const testKey = "test-key-0123456789"

type fakeManager struct {
	sessions map[string]wa.Info
	sent     []wa.SendTextRequest
	qr       string
}

func (f *fakeManager) Create(_ context.Context, id string) (wa.Info, error) {
	if _, ok := f.sessions[id]; ok {
		return wa.Info{}, wa.ErrExists
	}
	if id == "" {
		return wa.Info{}, wa.ErrInvalid
	}
	f.sessions[id] = wa.Info{ID: id, Status: wa.StatusCreated}
	return f.sessions[id], nil
}
func (f *fakeManager) List() []wa.Info {
	var out []wa.Info
	for _, s := range f.sessions {
		out = append(out, s)
	}
	return out
}
func (f *fakeManager) Get(id string) (wa.Info, error) {
	s, ok := f.sessions[id]
	if !ok {
		return wa.Info{}, wa.ErrNotFound
	}
	return s, nil
}
func (f *fakeManager) Connect(_ context.Context, id string) (wa.Info, error) {
	s, err := f.Get(id)
	s.Status = wa.StatusQRReady
	f.qr = "2@fake-qr"
	return s, err
}
func (f *fakeManager) QR(id string) (string, error) {
	if _, err := f.Get(id); err != nil {
		return "", err
	}
	if f.qr == "" {
		return "", wa.ErrNoQR
	}
	return f.qr, nil
}
func (f *fakeManager) PairingCode(_ context.Context, id, phone string) (string, error) {
	return "ABCD-EFGH", nil
}
func (f *fakeManager) Disconnect(id string) (wa.Info, error)                { return f.Get(id) }
func (f *fakeManager) Logout(_ context.Context, id string) (wa.Info, error) { return f.Get(id) }
func (f *fakeManager) Delete(_ context.Context, id string) error {
	if _, err := f.Get(id); err != nil {
		return err
	}
	delete(f.sessions, id)
	return nil
}
func (f *fakeManager) SendText(_ context.Context, id string, req wa.SendTextRequest) (store.Message, error) {
	s, err := f.Get(id)
	if err != nil {
		return store.Message{}, err
	}
	if s.Status != wa.StatusReady {
		return store.Message{}, wa.ErrNotReady
	}
	f.sent = append(f.sent, req)
	return store.Message{ID: "MSG1", ChatID: req.ChatID, FromMe: true, Type: "text", Text: req.Text, Status: "sent"}, nil
}
func (f *fakeManager) ListChats(_ context.Context, id string, archived bool, limit, offset int) ([]store.Chat, error) {
	if archived {
		return []store.Chat{{ID: "2@s.whatsapp.net", Name: "Bob", Archived: true}}, nil
	}
	return []store.Chat{{ID: "1@s.whatsapp.net", Name: "Alice"}}, nil
}
func (f *fakeManager) ChatSummary(_ context.Context, id string) (store.ChatSummary, error) {
	return store.ChatSummary{Archived: 1}, nil
}
func (f *fakeManager) ListMessages(_ context.Context, id, chatID string, before int64, limit int) ([]store.Message, error) {
	return []store.Message{{ID: "x", ChatID: chatID, Timestamp: before, Type: "text", Text: strings.Repeat("a", limit)}}, nil
}

func (f *fakeManager) Media(_ context.Context, id, chatID, msgID string, thumb bool) (wa.Media, error) {
	switch msgID {
	case "img":
		return wa.Media{Data: []byte("\xff\xd8jpeg"), Mimetype: "image/jpeg"}, nil
	case "doc":
		return wa.Media{Data: []byte("<script>alert(1)</script>"), Mimetype: "text/html", FileName: "évil.html"}, nil
	case "old":
		return wa.Media{}, wa.ErrMediaExpired
	}
	return wa.Media{}, wa.ErrNoMedia
}

func (f *fakeManager) Revoke(_ context.Context, id, chatID, msgID string) (store.Message, error) {
	if msgID == "theirs" {
		return store.Message{}, wa.ErrInvalid
	}
	return store.Message{ID: msgID, ChatID: chatID, FromMe: true, Type: store.TypeRevoked}, nil
}

func (f *fakeManager) SetChatArchived(_ context.Context, id, chatID string, archive bool) (wa.ChatFlags, error) {
	return wa.ChatFlags{ChatID: chatID, Archived: archive}, nil
}
func (f *fakeManager) SetChatPinned(_ context.Context, id, chatID string, pin bool) (wa.ChatFlags, error) {
	if pin && chatID == "full@s.whatsapp.net" {
		return wa.ChatFlags{}, wa.ErrInvalid
	}
	return wa.ChatFlags{ChatID: chatID, Pinned: pin}, nil
}

func (f *fakeManager) SendMedia(_ context.Context, id string, req wa.SendMediaRequest) (store.Message, error) {
	if len(req.Data) == 0 {
		return store.Message{}, wa.ErrInvalid
	}
	return store.Message{ID: "M1", ChatID: req.ChatID, FromMe: true, Type: "document",
		Text: req.Caption + "|" + req.FileName + "|" + string(req.Data)}, nil
}

func (f *fakeManager) ReloadMedia(_ context.Context, id, chatID, msgID string) (store.Message, error) {
	if msgID == "slow" {
		return store.Message{}, wa.ErrReloadTimeout
	}
	return store.Message{ID: msgID, ChatID: chatID, Type: "image", Media: &store.MediaInfo{Mimetype: "image/jpeg"}}, nil
}

func TestReloadMedia(t *testing.T) {
	ts, f, _ := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	base := "/sessions/main/chats/1@s.whatsapp.net/messages/"
	if resp, body := do(t, ts, "POST", base+"old/reload-media", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), `"mimetype":"image/jpeg"`) {
		t.Fatalf("reload: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, ts, "POST", base+"slow/reload-media", "", true); resp.StatusCode != 504 || !strings.Contains(string(body), "RELOAD_TIMEOUT") {
		t.Fatalf("timeout: %d %s", resp.StatusCode, body)
	}
}

func TestSendMedia(t *testing.T) {
	ts, f, _ := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chatId", "628123456")
	_ = mw.WriteField("caption", "hi")
	fw, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/sessions/main/messages/send-media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `hi|note.txt|hello`) {
		t.Fatalf("send-media: %d %s", resp.StatusCode, b)
	}
	if resp, _ := do(t, ts, "POST", "/sessions/main/messages/send-media", `{"chatId":"1"}`, true); resp.StatusCode != 400 {
		t.Fatalf("json body: %d", resp.StatusCode)
	}
}

func TestChatActions(t *testing.T) {
	ts, f, _ := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	for path, want := range map[string]string{
		"archive": `"archived":true`, "unarchive": `"archived":false`, "pin": `"pinned":true`, "unpin": `"pinned":false`,
	} {
		resp, body := do(t, ts, "POST", "/sessions/main/chats/1@s.whatsapp.net/"+path, "", true)
		if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	if resp, _ := do(t, ts, "POST", "/sessions/main/chats/full@s.whatsapp.net/pin", "", true); resp.StatusCode != 400 {
		t.Fatalf("pin over limit: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "POST", "/sessions/main/chats/1@s.whatsapp.net/pin", "", false); resp.StatusCode != 401 {
		t.Fatalf("pin without key: %d", resp.StatusCode)
	}
}

func TestRevoke(t *testing.T) {
	ts, f, _ := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	base := "/sessions/main/chats/1@s.whatsapp.net/messages/"
	if resp, body := do(t, ts, "DELETE", base+"mine", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), `"type":"revoked"`) {
		t.Fatalf("revoke own: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, ts, "DELETE", base+"theirs", "", true); resp.StatusCode != 400 {
		t.Fatalf("revoke other's: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "DELETE", base+"mine", "", false); resp.StatusCode != 401 {
		t.Fatalf("revoke without key: %d", resp.StatusCode)
	}
}

func TestMedia(t *testing.T) {
	ts, f, _ := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	base := "/sessions/main/chats/1@s.whatsapp.net/messages/"
	resp, body := do(t, ts, "GET", base+"img/media", "", true)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || resp.Header.Get("Content-Disposition") != "" || string(body) != "\xff\xd8jpeg" {
		t.Fatalf("image: %d %v", resp.StatusCode, resp.Header)
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("media served without sandbox CSP")
	}
	resp, _ = do(t, ts, "GET", base+"doc/media", "", true)
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("html document must download, got %v", resp.Header)
	}
	if resp, body := do(t, ts, "GET", base+"old/media", "", true); resp.StatusCode != 410 || !strings.Contains(string(body), "MEDIA_EXPIRED") {
		t.Fatalf("expired: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, ts, "GET", base+"none/media", "", true); resp.StatusCode != 404 {
		t.Fatalf("no media: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "GET", base+"img/media", "", false); resp.StatusCode != 401 {
		t.Fatalf("media without key: %d", resp.StatusCode)
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeManager, *hub.Hub) {
	t.Helper()
	f := &fakeManager{sessions: map[string]wa.Info{}}
	h := hub.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(f, h, testKey, log).Handler())
	t.Cleanup(ts.Close)
	return ts, f, h
}

func do(t *testing.T, ts *httptest.Server, method, path, body string, key bool) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, r)
	if key {
		req.Header.Set("X-API-Key", testKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestAuth(t *testing.T) {
	ts, _, _ := newTestServer(t)
	if resp, _ := do(t, ts, "GET", "/health", "", false); resp.StatusCode != 200 {
		t.Fatalf("/health without key: %d", resp.StatusCode)
	}
	if resp, body := do(t, ts, "GET", "/", "", false); resp.StatusCode != 200 || !strings.Contains(string(body), "Link WhatsApp") {
		t.Fatalf("/ without key: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "GET", "/nope", "", true); resp.StatusCode != 404 {
		t.Fatalf("unknown path: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "GET", "/sessions", "", false); resp.StatusCode != 401 {
		t.Fatalf("/sessions without key: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/sessions", nil)
	req.Header.Set("X-API-Key", "wrong")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
	// ?api_key= is only accepted on /events.
	if resp, _ := do(t, ts, "GET", "/sessions?api_key="+testKey, "", false); resp.StatusCode != 401 {
		t.Fatalf("query key on REST route: %d", resp.StatusCode)
	}
}

func TestSessionFlow(t *testing.T) {
	ts, f, _ := newTestServer(t)
	resp, body := do(t, ts, "POST", "/sessions", `{"id":"main"}`, true)
	if resp.StatusCode != 201 || !strings.Contains(string(body), `"status":"created"`) {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, ts, "POST", "/sessions", `{"id":"main"}`, true); resp.StatusCode != 409 || !strings.Contains(string(body), "SESSION_EXISTS") {
		t.Fatalf("duplicate: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, ts, "POST", "/sessions", `not json`, true); resp.StatusCode != 400 {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
	if resp, body := do(t, ts, "GET", "/sessions/main/qr", "", true); resp.StatusCode != 404 || !strings.Contains(string(body), "NO_QR") {
		t.Fatalf("qr before connect: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, ts, "POST", "/sessions/main/connect", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), "qr_ready") {
		t.Fatalf("connect: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, ts, "GET", "/sessions/main/qr", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), "2@fake-qr") {
		t.Fatalf("qr json: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, ts, "GET", "/sessions/main/qr?format=png", "", true)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("qr png: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, body := do(t, ts, "POST", "/sessions/main/messages/send-text", `{"chatId":"1","text":"hi"}`, true); resp.StatusCode != 409 || !strings.Contains(string(body), "SESSION_NOT_READY") {
		t.Fatalf("send before ready: %d %s", resp.StatusCode, body)
	}

	f.sessions["main"] = wa.Info{ID: "main", Status: wa.StatusReady}
	resp, body = do(t, ts, "POST", "/sessions/main/messages/send-text", `{"chatId":"628123456","text":"hi","replyTo":"Q"}`, true)
	if resp.StatusCode != 200 {
		t.Fatalf("send: %d %s", resp.StatusCode, body)
	}
	var msg store.Message
	_ = json.Unmarshal(body, &msg)
	if msg.ID != "MSG1" || len(f.sent) != 1 || f.sent[0].ReplyTo != "Q" {
		t.Fatalf("send result %+v, sent %+v", msg, f.sent)
	}

	if resp, _ := do(t, ts, "GET", "/sessions/nope", "", true); resp.StatusCode != 404 {
		t.Fatalf("missing session: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "DELETE", "/sessions/main", "", true); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestChatsAndMessagesParams(t *testing.T) {
	ts, f, _ := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	if resp, body := do(t, ts, "GET", "/sessions/main/chats", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), "Alice") {
		t.Fatalf("chats: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, ts, "GET", "/sessions/main/chats?archived=true", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), "Bob") {
		t.Fatalf("archived chats: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, ts, "GET", "/sessions/main/chats?archived=maybe", "", true); resp.StatusCode != 400 {
		t.Fatalf("bad archived param: %d", resp.StatusCode)
	}
	if resp, body := do(t, ts, "GET", "/sessions/main/chats/summary", "", true); resp.StatusCode != 200 || !strings.Contains(string(body), `"archived":1`) {
		t.Fatalf("summary: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, ts, "GET", "/sessions/main/chats?limit=-1", "", true); resp.StatusCode != 400 {
		t.Fatalf("negative limit: %d", resp.StatusCode)
	}
	resp, body := do(t, ts, "GET", "/sessions/main/chats/1@s.whatsapp.net/messages?limit=1000&before=42", "", true)
	if resp.StatusCode != 200 {
		t.Fatalf("messages: %d %s", resp.StatusCode, body)
	}
	var msgs []store.Message
	_ = json.Unmarshal(body, &msgs)
	if len(msgs) != 1 || msgs[0].ChatID != "1@s.whatsapp.net" || msgs[0].Timestamp != 42 || len(msgs[0].Text) != maxLimit {
		t.Fatalf("params not passed through (limit should clamp to %d): %+v", maxLimit, msgs)
	}
}

func TestEventsWebSocket(t *testing.T) {
	ts, f, h := newTestServer(t)
	f.sessions["main"] = wa.Info{ID: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/events?session=main&api_key=" + testKey
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	// Wait for the subscription to register before publishing.
	for h.Len() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	h.Publish("other", hub.MessageReceived, "ignored")
	h.Publish("main", hub.MessageReceived, map[string]string{"text": "hello"})

	var ev hub.Event
	if err := wsjson.Read(ctx, c, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Event != hub.MessageReceived || ev.Session != "main" {
		t.Fatalf("event = %+v", ev)
	}

	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/events", nil); err == nil {
		t.Fatal("dial without key succeeded")
	}
}
