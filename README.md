# mywa

A lightweight, self-hosted WhatsApp HTTP API written in Go. It is one binary with
one SQLite file and needs no browser.

It covers a small part of [OpenWA](https://github.com/rmyndharis/OpenWA):
- link a number by QR code or pairing code
- list chats and read a chat's messages
- send text messages
- receive messages in real time over a WebSocket
- disconnect or log out

It runs on [whatsmeow](https://github.com/tulir/whatsmeow), the Go implementation
of WhatsApp's multi-device protocol.

> ⚠️ **This is an unofficial client.** WhatsApp may restrict or ban numbers that use
> unofficial automation. Use a dedicated number, never your main one, and don't
> bulk-message people who haven't messaged you. For regulated or business-critical
> use, choose Meta's official WhatsApp Cloud API.

## Quick start

```sh
export MYWA_API_KEY=$(openssl rand -hex 24)   # required, 16+ characters
go run ./cmd/mywa                              # or: make run
```

With Docker:

```sh
docker build -t mywa .
docker run -d -p 8080:8080 -e MYWA_API_KEY=$MYWA_API_KEY -v mywa-data:/data mywa
```

| Variable | Default | |
|---|---|---|
| `MYWA_API_KEY` | (required) | Sent as the `X-API-Key` header |
| `MYWA_ADDR` | `:8080` | Listen address |
| `MYWA_DB_PATH` | `mywa.db` | SQLite file. It holds the WhatsApp credentials, so protect it like a password. |
| `MYWA_MEDIA_DIR` | `media` next to the database | Cache of downloaded attachments |
| `MYWA_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

## Link from the browser

Open the server's root URL, e.g. `http://localhost:8080/`. The page asks for the API
key, creates session `main` if needed, and shows the QR code, which refreshes live as
WhatsApp rotates it. Once you scan it, the page switches to your chats:

- **Wide screens (900 px and up):** chat list on the left, the open chat on the right.
- **Phones:** the chat list, and tapping a chat opens it full screen (← or Back returns).
- The list is newest activity first, with unread counts, a filter box, and live updates.
- In a chat, scroll up to load older messages. Type in the box at the bottom to send:
  Enter sends, Shift+Enter adds a new line.
- Photos and stickers load as you scroll to them (click a photo to open it full size).
  Videos, GIFs and voice notes play inline when tapped; documents download.
- The ⋮ menu has Disconnect and Log out.

- Append `#key=<api key>` to the URL to skip the prompt. Browsers don't send the
  part after `#` to the server, and the page removes it from the address bar.
- Add `?session=<id>` to link a session other than `main`.
- The page itself holds no data; everything it does goes through the API with your key.

## Walkthrough

```sh
K="X-API-Key: $MYWA_API_KEY"; B=http://localhost:8080

# 1. Create a session and connect it
curl -H "$K" -X POST $B/sessions -d '{"id":"main"}'
curl -H "$K" -X POST $B/sessions/main/connect          # → "status": "qr_ready"

# 2. Link it: scan the QR with WhatsApp → Settings → Linked devices → Link a device
curl -H "$K" "$B/sessions/main/qr?format=png" -o qr.png
#    ...or use a pairing code instead of the QR:
curl -H "$K" -X POST $B/sessions/main/pairing-code -d '{"phone":"6281234567890"}'

curl -H "$K" $B/sessions/main                          # → "status": "ready"

# 3. Read
curl -H "$K" "$B/sessions/main/chats?limit=20"
curl -H "$K" "$B/sessions/main/chats/6281234567890@s.whatsapp.net/messages?limit=50"

# 4. Send
curl -H "$K" -X POST $B/sessions/main/messages/send-text \
     -d '{"chatId":"6281234567890","text":"Hello from mywa"}'

# 5. Stop
curl -H "$K" -X POST $B/sessions/main/disconnect       # keeps the login; connect again without a QR
curl -H "$K" -X POST $B/sessions/main/logout           # unlinks the device from the phone
```

QR codes rotate about every 20 seconds, so fetch `/qr` again or watch the
`session.qr` events. A session that is `ready` when the server stops reconnects
on its own at the next start.

## REST API

All routes except `GET /health` need the `X-API-Key` header. Errors look like
`{"error": "...", "code": "SESSION_NOT_READY"}`.

| Method | Path | Notes |
|---|---|---|
| GET | `/` | Browser page for linking by QR (see above), no auth |
| GET | `/health` | Liveness check, no auth |
| POST | `/sessions` | `{"id": "main"}`. The id is 1–64 characters of `A-Za-z0-9_-`. |
| GET | `/sessions` | All sessions |
| GET | `/sessions/{id}` | `{id, status, jid, pushName, error, createdAt}` |
| DELETE | `/sessions/{id}` | Logs out and deletes all data for the session |
| POST | `/sessions/{id}/connect` | Waits up to 15 s for `qr_ready` or `ready` |
| GET | `/sessions/{id}/qr` | `{"code": "..."}`; `?format=png` returns a 512 px PNG |
| POST | `/sessions/{id}/pairing-code` | `{"phone": "62812..."}` → `{"code": "ABCD-EFGH"}`. Only works in `qr_ready`. |
| POST | `/sessions/{id}/disconnect` | Closes the connection and keeps the credentials |
| POST | `/sessions/{id}/logout` | Unlinks the device and clears the stored chats |
| GET | `/sessions/{id}/chats` | `?limit=50&offset=0` (max 200), sorted by most recent activity |
| GET | `/sessions/{id}/chats/{chatId}/messages` | `?limit=50&before=<unix ts>`, newest first. To page back, pass the oldest `timestamp` you have as `before`. |
| GET | `/sessions/{id}/chats/{chatId}/messages/{msgId}/media` | The attachment of an image, video, GIF, voice/audio, document or sticker message. `?thumb=1` returns its small embedded JPEG preview. Images, audio and video are served inline; everything else is sent as a download. `410 MEDIA_EXPIRED` means WhatsApp no longer has the file. |
| POST | `/sessions/{id}/messages/send-text` | `{"chatId", "text", "replyTo"?}`. `replyTo` is the id of a stored message in that chat. |

`chatId` can be a phone number in international format (`6281234567890`,
`+62 812-3456-7890`) or a full JID:
- `…@s.whatsapp.net` for a person
- `…@g.us` for a group
- `…@lid` for WhatsApp's privacy id. mywa maps it to the phone number when that is known.

### Session status

| Status | Meaning |
|---|---|
| `created` | Never linked, or the QR expired |
| `connecting` | Connecting or reconnecting |
| `qr_ready` | Waiting for a QR scan or pairing code |
| `ready` | Connected and logged in |
| `disconnected` | Linked but stopped. `connect` resumes without a QR. |
| `logged_out` | Unlinked. `connect` shows a new QR. |
| `failed` | Stopped by an error, for example a temporary ban, an outdated client or another process using the same login. See `error`. |
| `action_required` | WhatsApp asked for passkey verification, which mywa can't perform |

### Message object

```json
{
  "id": "3EB0C767D26A1D8F5F3A",
  "chatId": "6281234567890@s.whatsapp.net",
  "senderId": "6281234567890@s.whatsapp.net",
  "senderName": "Alice",
  "fromMe": false,
  "timestamp": 1731830400,
  "type": "text",
  "text": "hi!",
  "status": "received",
  "quotedId": "",
  "edited": false
}
```

- `type`: `text`, `image`, `video`, `gif`, `audio`, `voice`, `document`, `sticker`,
  `location`, `contact`, `poll`, `event`, `revoked` or `unknown`.
- `text`: the text, a media caption, a document's file name, a location's name or a poll's question.
- `media` (present on image, video, GIF, audio, voice, document and sticker messages):
  `{mimetype, fileName, size, width, height, seconds, hasThumb}`. Fetch the file from the
  `/media` route. Messages stored before mywa kept download details have no `media`.
- `status` of your own messages moves forward through `sent → delivered → read → played`.
  Incoming messages are always `received`.

## Real-time events

```
GET /events                  (WebSocket; X-API-Key header, or ?api_key= for browsers)
GET /events?session=main     (only events for one session)
```

```js
const ws = new WebSocket(`ws://localhost:8080/events?session=main&api_key=${KEY}`);
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

Every frame is `{"event", "session", "timestamp", "data"}`:

| Event | `data` |
|---|---|
| `session.status` | The session object |
| `session.qr` | `{code, expiresIn}` |
| `message.received` | A message object |
| `message.sent` | A message object (sent through the API, or from your phone) |
| `message.ack` | `{chatId, ids, status, timestamp}` |
| `message.edited` | `{chatId, id, text}` |
| `message.revoked` | `{chatId, id}` |

A client that falls 256 events behind is disconnected with close code 1008;
reconnect and read the REST API to catch up.

## How chats get there

WhatsApp has no "list my chats" request. mywa builds its own chat list from three
sources:
- the history sync your phone sends right after linking (recent chats, names,
  unread counts and recent messages)
- every message received since
- every message sent through the API

So the list holds your recent chats and everything since linking, not your full
history. Unread counts follow what you read on the phone.

## Development

```sh
make test     # go test -race ./...
make vet
```

```
cmd/mywa         entry point: config, DB, manager, HTTP server, graceful shutdown
internal/config  environment variables
internal/store   SQLite: sessions, chats, messages (whatsmeow keeps whatsmeow_* tables in the same file)
internal/wa      session manager, whatsmeow client wrapper, event handler, message conversion
internal/hub     event fan-out to WebSocket subscribers
internal/api     REST handlers, auth, /events WebSocket
```

Keep whatsmeow current (`go get -u go.mau.fi/whatsmeow && go mod tidy`). WhatsApp
changes its protocol often, and an outdated client shows up as status `failed`
with an "outdated" error.

## Not in v1

Media sending, groups management, reactions, webhooks and the
dashboard. See [PLAN.md](PLAN.md).
