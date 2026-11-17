package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/rockcacing/mywa/internal/hub"
	mystore "github.com/rockcacing/mywa/internal/store"
)

// Status is a session's connection state.
type Status string

const (
	StatusCreated        Status = "created"         // never linked, or linking was abandoned
	StatusConnecting     Status = "connecting"      // connecting or reconnecting to WhatsApp
	StatusQRReady        Status = "qr_ready"        // waiting for a QR scan or pairing code
	StatusReady          Status = "ready"           // connected and logged in
	StatusDisconnected   Status = "disconnected"    // linked, but not connected (credentials kept)
	StatusLoggedOut      Status = "logged_out"      // device unlinked; connect shows a new QR
	StatusFailed         Status = "failed"          // stopped by an error; see Info.Error
	StatusActionRequired Status = "action_required" // WhatsApp wants a step mywa can't do (passkey)
)

const maxTextLen = 65536

// Session is one linked (or linkable) WhatsApp account.
type Session struct {
	id      string
	created time.Time
	m       *Manager
	log     *slog.Logger

	mu       sync.Mutex
	client   *whatsmeow.Client
	status   Status
	jid      string
	pushName string
	qr       string
	lastErr  string
	qrCancel context.CancelFunc
	restore  bool

	historyMu    sync.Mutex // serialises history-sync batches
	groupLookups sync.Map   // chat JID → struct{}: group-name lookups already started
}

func newSession(m *Manager, r mystore.SessionRow) *Session {
	s := &Session{
		id:       r.ID,
		created:  r.CreatedAt,
		m:        m,
		log:      m.log.With("session", r.ID),
		jid:      r.JID,
		pushName: r.PushName,
	}
	switch {
	case r.JID != "":
		s.status = StatusDisconnected
		s.restore = r.Status == string(StatusReady) || r.Status == string(StatusConnecting)
	case r.Status == string(StatusLoggedOut):
		s.status = StatusLoggedOut
	default:
		s.status = StatusCreated
	}
	return s
}

func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *Session) infoLocked() Info {
	return Info{ID: s.id, Status: s.status, JID: accountJID(s.jid), PushName: s.pushName, Error: s.lastErr, CreatedAt: s.created.Unix()}
}

// accountJID turns the stored device JID ("6012...:12@s.whatsapp.net") into the
// account JID callers expect ("6012...@s.whatsapp.net").
func accountJID(deviceJID string) string {
	if deviceJID == "" {
		return ""
	}
	j, err := types.ParseJID(deviceJID)
	if err != nil {
		return deviceJID
	}
	return j.ToNonAD().String()
}

func (s *Session) shouldRestore() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restore
}

// setStatus records, persists and broadcasts a status change.
func (s *Session) setStatus(st Status, errMsg string) {
	s.mu.Lock()
	changed := s.status != st || s.lastErr != errMsg
	s.status, s.lastErr = st, errMsg
	if st != StatusQRReady {
		s.qr = ""
	}
	info := s.infoLocked()
	s.mu.Unlock()
	if !changed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.m.db.SetSessionStatus(ctx, s.id, string(st)); err != nil {
		s.log.Error("persist status", "err", err)
	}
	s.log.Info("status", "status", st, "error", errMsg)
	s.m.hub.Publish(s.id, hub.SessionStatus, info)
}

func (s *Session) setAccount(jid, pushName string) {
	s.mu.Lock()
	s.jid, s.pushName = jid, pushName
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.m.db.SetSessionAccount(ctx, s.id, jid, pushName); err != nil {
		s.log.Error("persist account", "err", err)
	}
}

// isCurrent reports whether cli is still this session's live client. Events from a
// client that has been replaced or disconnected are ignored.
func (s *Session) isCurrent(cli *whatsmeow.Client) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client == cli
}

// detach forgets the current client and stops its QR loop, returning it so the caller
// can disconnect it outside the lock.
func (s *Session) detachLocked() *whatsmeow.Client {
	cli := s.client
	s.client = nil
	if s.qrCancel != nil {
		s.qrCancel()
		s.qrCancel = nil
	}
	return cli
}

// Connect starts the session. An unlinked session moves to qr_ready (scan the QR or
// request a pairing code); a linked one reconnects with its saved credentials.
// It waits up to 15 s for either outcome and returns the session's state.
func (s *Session) Connect(ctx context.Context) (Info, error) {
	s.mu.Lock()
	if s.client != nil {
		info := s.infoLocked()
		s.mu.Unlock()
		return info, nil // already running (connecting, qr_ready or ready)
	}
	device, err := s.deviceLocked(ctx)
	if err != nil {
		s.mu.Unlock()
		return Info{}, err
	}
	cli := whatsmeow.NewClient(device, newWALogger(s.log.With("component", "whatsmeow")))
	cli.EnableAutoReconnect = true
	cli.AddEventHandler(func(evt any) { s.handleEvent(cli, evt) })
	if device.ID == nil {
		qrCtx, cancel := context.WithCancel(context.Background())
		ch, err := cli.GetQRChannel(qrCtx)
		if err != nil {
			cancel()
			s.mu.Unlock()
			return Info{}, fmt.Errorf("qr channel: %w", err)
		}
		s.qrCancel = cancel
		go s.qrLoop(cli, ch)
	}
	s.client = cli
	s.restore = false
	s.mu.Unlock()

	s.setStatus(StatusConnecting, "")
	if err := cli.Connect(); err != nil {
		s.mu.Lock()
		if s.client == cli {
			s.detachLocked()
		}
		s.mu.Unlock()
		s.setStatus(StatusFailed, "connect: "+err.Error())
		return s.Info(), nil
	}
	return s.waitSettled(ctx, 15*time.Second), nil
}

// deviceLocked returns the saved device for a linked session, or a fresh one.
func (s *Session) deviceLocked(ctx context.Context) (*store.Device, error) {
	if s.jid != "" {
		dev, err := s.findDevice(ctx, s.jid)
		if err != nil {
			return nil, fmt.Errorf("load device: %w", err)
		}
		if dev != nil {
			return dev, nil
		}
		s.log.Warn("saved device missing; a new link is needed", "jid", s.jid)
	}
	return s.m.container.NewDevice(), nil
}

// findDevice looks up whatsmeow's stored device for jid. whatsmeow keys devices by the
// full device JID (user:device@server). Older mywa versions saved only the account JID,
// so when there is no exact match, fall back to the stored device of the same account.
func (s *Session) findDevice(ctx context.Context, jidStr string) (*store.Device, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, nil
	}
	if jid.Device != 0 {
		return s.m.container.GetDevice(ctx, jid)
	}
	all, err := s.m.container.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range all {
		if d.ID != nil && d.ID.User == jid.User && d.ID.Server == jid.Server {
			return d, nil
		}
	}
	return nil, nil
}

func (s *Session) waitSettled(ctx context.Context, timeout time.Duration) Info {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		info := s.Info()
		if info.Status != StatusConnecting {
			return info
		}
		select {
		case <-ctx.Done():
			return info
		case <-deadline.C:
			return info
		case <-tick.C:
		}
	}
}

func (s *Session) qrLoop(cli *whatsmeow.Client, ch <-chan whatsmeow.QRChannelItem) {
	for item := range ch {
		if !s.isCurrent(cli) {
			return
		}
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			s.mu.Lock()
			s.qr = item.Code
			s.mu.Unlock()
			s.setStatus(StatusQRReady, "")
			s.m.hub.Publish(s.id, hub.SessionQR, map[string]any{
				"code": item.Code, "expiresIn": int(item.Timeout.Seconds()),
			})
		case whatsmeow.QRChannelSuccess.Event:
			s.setStatus(StatusConnecting, "")
		case whatsmeow.QRChannelTimeout.Event:
			s.stop(cli)
			s.setStatus(StatusCreated, "QR code expired without a scan; call connect again")
		case whatsmeow.QRChannelEventPasskeyRequest, whatsmeow.QRChannelEventPasskeyResponse:
			s.stop(cli)
			s.setStatus(StatusActionRequired,
				"WhatsApp asked for passkey verification to link this device; mywa does not support that step")
		case whatsmeow.QRChannelEventError:
			s.stop(cli)
			s.setStatus(StatusFailed, fmt.Sprintf("pairing failed: %v", item.Error))
		default:
			s.stop(cli)
			s.setStatus(StatusFailed, "pairing failed: "+item.Event)
		}
	}
}

// stop disconnects cli if it is still the current client.
func (s *Session) stop(cli *whatsmeow.Client) {
	s.mu.Lock()
	if s.client != cli {
		s.mu.Unlock()
		return
	}
	s.detachLocked()
	s.mu.Unlock()
	go cli.Disconnect() // may be called from inside a whatsmeow callback
}

func (s *Session) QR() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != StatusQRReady || s.qr == "" {
		return "", ErrNoQR
	}
	return s.qr, nil
}

// PairingCode links by phone number instead of QR. The session must be in qr_ready.
func (s *Session) PairingCode(ctx context.Context, phone string) (string, error) {
	jid, err := ParseChatID(phone)
	if err != nil || jid.Server != types.DefaultUserServer || strings.Contains(phone, "@") {
		return "", fmt.Errorf("%w: phone must be a number in international format, e.g. 6281234567890", ErrInvalid)
	}
	s.mu.Lock()
	cli, st := s.client, s.status
	s.mu.Unlock()
	if cli == nil || st != StatusQRReady {
		return "", fmt.Errorf("%w: call connect first and wait for status qr_ready", ErrState)
	}
	code, err := cli.PairPhone(ctx, jid.User, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", fmt.Errorf("%w: pair phone: %v", ErrUpstream, err)
	}
	return code, nil
}

// Disconnect closes the connection but keeps the credentials.
func (s *Session) Disconnect() Info {
	s.mu.Lock()
	cli := s.detachLocked()
	s.restore = false
	linked := s.jid != ""
	s.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	if linked {
		s.setStatus(StatusDisconnected, "")
	} else {
		s.setStatus(StatusCreated, "")
	}
	return s.Info()
}

// shutdown disconnects for process exit without touching the persisted status.
func (s *Session) shutdown() {
	s.mu.Lock()
	cli := s.detachLocked()
	s.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
}

// Logout unlinks the device from the phone and clears the session's chats and messages.
func (s *Session) Logout(ctx context.Context) (Info, error) {
	s.mu.Lock()
	cli, jid := s.client, s.jid
	s.mu.Unlock()
	if jid == "" {
		return s.Info(), fmt.Errorf("%w: session is not linked; use disconnect to stop a pending QR login", ErrState)
	}
	remote := false
	if cli != nil && cli.IsConnected() && cli.IsLoggedIn() {
		if err := cli.Logout(ctx); err != nil {
			s.log.Warn("remote logout failed; removing local credentials only", "err", err)
		} else {
			remote = true
		}
	}
	s.unlink(ctx, !remote)
	s.setStatus(StatusLoggedOut, "")
	return s.Info(), nil
}

// unlink drops the client, the saved device (when deleteDevice is set) and the
// session's stored chats and messages.
func (s *Session) unlink(ctx context.Context, deleteDevice bool) {
	s.mu.Lock()
	cli := s.detachLocked()
	jid := s.jid
	s.restore = false
	s.mu.Unlock()
	if cli != nil {
		go cli.Disconnect()
	}
	if deleteDevice && jid != "" {
		if dev, err := s.findDevice(ctx, jid); err == nil && dev != nil {
			if err := s.m.container.DeleteDevice(ctx, dev); err != nil {
				s.log.Warn("delete device", "err", err)
			}
		}
	}
	s.setAccount("", "")
	if err := s.m.db.PurgeSessionData(ctx, s.id); err != nil {
		s.log.Error("purge session data", "err", err)
	}
}

func (s *Session) readyClient() (*whatsmeow.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.status != StatusReady || !s.client.IsLoggedIn() {
		return nil, ErrNotReady
	}
	return s.client, nil
}

func (s *Session) currentClient() *whatsmeow.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// SendText sends a text message, optionally as a reply to a stored message of the same chat.
func (s *Session) SendText(ctx context.Context, req SendTextRequest) (mystore.Message, error) {
	if strings.TrimSpace(req.Text) == "" {
		return mystore.Message{}, fmt.Errorf("%w: text is required", ErrInvalid)
	}
	if utf8.RuneCountInString(req.Text) > maxTextLen {
		return mystore.Message{}, fmt.Errorf("%w: text is longer than %d characters", ErrInvalid, maxTextLen)
	}
	to, err := ParseChatID(req.ChatID)
	if err != nil {
		return mystore.Message{}, err
	}
	cli, err := s.readyClient()
	if err != nil {
		return mystore.Message{}, err
	}
	chat := s.canonicalChat(ctx, to, types.EmptyJID)

	msg := &waE2E.Message{Conversation: proto.String(req.Text)}
	if req.ReplyTo != "" {
		q, err := s.m.db.GetMessage(ctx, s.id, chat.String(), req.ReplyTo)
		if errors.Is(err, mystore.ErrNotFound) {
			return mystore.Message{}, fmt.Errorf("%w: replyTo message %q not found in this chat", ErrInvalid, req.ReplyTo)
		} else if err != nil {
			return mystore.Message{}, err
		}
		msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(req.Text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(q.ID),
				Participant:   proto.String(q.SenderID),
				QuotedMessage: &waE2E.Message{Conversation: proto.String(q.Text)},
			},
		}}
	}

	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return mystore.Message{}, fmt.Errorf("%w: send: %v", ErrUpstream, err)
	}
	info := s.Info()
	m := mystore.Message{
		ID: resp.ID, ChatID: chat.String(), SenderID: info.JID, SenderName: info.PushName,
		FromMe: true, Timestamp: resp.Timestamp.Unix(), Type: TypeText, Text: req.Text,
		Status: mystore.StatusSent, QuotedID: req.ReplyTo,
	}
	if m.Timestamp <= 0 {
		m.Timestamp = nowUnix()
	}
	if _, err := s.m.db.SaveMessage(ctx, s.id, m, false); err != nil {
		s.log.Error("store sent message", "err", err)
	}
	s.ensureChatName(ctx, cli, chat, "")
	s.m.hub.Publish(s.id, hub.MessageSent, m)
	return m, nil
}

// canonicalChat maps a person's privacy id (@lid) to their phone-number JID when it is
// known, so one person's messages land in one chat whichever address WhatsApp used.
// alt is the alternate address WhatsApp sent alongside the message, if any.
func (s *Session) canonicalChat(ctx context.Context, jid, alt types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if cli := s.currentClient(); cli != nil && cli.Store.LIDs != nil {
		if pn, err := cli.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid
}
