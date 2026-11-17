# mywa — Plan

> **Status:** milestones M1–M7 are built. Linking (QR) has been checked live against
> WhatsApp's servers. Sending and receiving with a linked phone still needs a manual
> check (see the README walkthrough). Where the build differs from this plan, the
> plan has been updated.

A lightweight, self-hosted WhatsApp HTTP API in Go. It covers a small part of
[OpenWA](https://github.com/rmyndharis/OpenWA): connect, list chats, view a chat,
send a message, receive messages in real time, and disconnect.

## 1. What we take from OpenWA, and what we leave out

OpenWA is a large NestJS gateway. It has two engines (whatsapp-web.js and
Baileys), webhooks with an outbox, a dashboard, plugins, Redis and S3.

**What we copy:**

| OpenWA idea | Where it lives in OpenWA | mywa version |
|---|---|---|
| Sessions identified by id, with REST routes under `/sessions/:id` | `src/modules/session/session.controller.ts` | Same URL shape |
| Session status machine `created → initializing → qr_ready → authenticating → ready → disconnected / failed` | `session.entity.ts` `SessionStatus` | A smaller set (see §4) |
| Separate **disconnect** (keep the login) and **logout** (unlink the device) | engine interface `disconnect()` / `logout()` | Same split |
| QR login and pairing-code login | `GET :id/qr`, `POST :id/pairing-code` | Same |
| Its own message and chat store, because the protocol engine has no "get chats" call | `baileys-message-store.service.ts`, `baileys-stored-chat-previews.service.ts` | SQLite tables `chats` and `messages` |
| Event names on a WebSocket: `message.received`, `message.sent`, `message.ack`, … | `src/modules/events/events.gateway.ts` | Same names, plain WebSocket instead of socket.io |
| API-key auth | `modules/auth` | One static key from an env var |

**What we leave out of v1:** the whatsapp-web.js / Chromium engine, the dashboard,
webhooks and their outbox, Redis, S3, plugins, groups admin, media sending,
reactions, edits, polls, bulk send, multi-tenant keys, and Postgres.

## 2. Stack

| Concern | Choice | Why |
|---|---|---|
| WhatsApp protocol | [`go.mau.fi/whatsmeow`](https://github.com/tulir/whatsmeow) | The mature Go multi-device client, the Go equivalent of Baileys. It needs no browser and uses about 20–50 MB per session. |
| DB | SQLite (`modernc.org/sqlite`, pure Go, no cgo) | One file, easy to ship. whatsmeow's `sqlstore` keeps the device keys in the same file. If its dialect handling gives trouble, we fall back to `mattn/go-sqlite3`. |
| HTTP | stdlib `net/http` (Go 1.22+ method/path patterns) | No framework needed |
| WebSocket | `github.com/coder/websocket` | Small, context-aware |
| QR PNG | `github.com/skip2/go-qrcode` | For `GET /qr?format=png` |
| Logging | `log/slog` | stdlib |
| Config | env vars (`MYWA_ADDR`, `MYWA_API_KEY`, `MYWA_DB_PATH`, `MYWA_LOG_LEVEL`) | Simple |

Go version: 1.23+.

## 3. Project layout

```
mywa/
├── cmd/mywa/main.go            # wiring, graceful shutdown, auto-reconnect of paired sessions on boot
├── internal/
│   ├── config/                 # env parsing
│   ├── wa/
│   │   ├── manager.go          # map[sessionID]*Session, create/get/delete
│   │   ├── session.go          # wraps *whatsmeow.Client: Connect, QR loop, PairPhone, Disconnect, Logout, SendText
│   │   ├── handler.go          # whatsmeow event → store write + hub publish
│   │   └── convert.go          # waE2E.Message → our Message DTO (text, caption, type)
│   ├── store/
│   │   ├── migrations.sql
│   │   ├── chats.go            # UpsertChat, ListChats
│   │   └── messages.go         # InsertMessage, ListMessages, UpdateStatus
│   ├── hub/hub.go              # pub/sub fan-out to WebSocket clients, filtered by session
│   └── api/
│       ├── router.go
│       ├── middleware.go       # API key, request log, recover
│       ├── sessions.go
│       ├── chats.go
│       ├── messages.go
│       └── ws.go               # GET /events
├── Dockerfile                  # multi-stage → distroless, about 20 MB image
├── Makefile
└── README.md
```

## 4. Session lifecycle

```
            POST /connect
 created ─────────────────► connecting ──(no stored device)──► qr_ready ──scan──► connecting ──► ready
                                │                                                                ▲
                                └─────────(stored device)────────────────────────────────────────┘
 qr_ready ──QR expired────► created
 ready ──POST /disconnect──► disconnected   (credentials kept; /connect again needs no QR)
 ready ──POST /logout─────► logged_out      (device unlinked; next /connect shows a new QR)
 ready ──network drop─────► connecting      (whatsmeow auto-reconnect)
 phone unlinks device ────► logged_out      (events.LoggedOut)
 ban / outdated / replaced► failed          (Info.error says why)
 passkey requested ───────► action_required (not supported)
```

- Status is held in memory and persisted in a `sessions` table, so paired sessions
  reconnect when the server starts.
- `events.Connected` → `ready`; `events.Disconnected` → `connecting` while
  auto-reconnect is on; `events.LoggedOut` → `logged_out`, and the device store is
  removed.
- QR codes come from `client.GetQRChannel()`. Each code is published as a
  `session.qr` event and kept as "current QR" for `GET /qr`.

## 5. REST API

All routes need the `X-API-Key: <key>` header, except `GET /health`.

| Method | Path | Body / query | Result |
|---|---|---|---|
| GET | `/health` | | `{ "ok": true }` |
| POST | `/sessions` | `{ "id": "main" }` | Creates a session record |
| GET | `/sessions` | | List with statuses |
| GET | `/sessions/{id}` | | `{ id, status, jid, pushName }` |
| POST | `/sessions/{id}/connect` | | Starts it; returns the status (`qr_ready` or `ready`) |
| GET | `/sessions/{id}/qr` | `?format=png` | `{ code }`, or a PNG image |
| POST | `/sessions/{id}/pairing-code` | `{ "phone": "62812..." }` | `{ code: "ABCD-EFGH" }` |
| POST | `/sessions/{id}/disconnect` | | Keeps the login |
| POST | `/sessions/{id}/logout` | | Unlinks the device |
| DELETE | `/sessions/{id}` | | Logs out and deletes all stored data |
| GET | `/sessions/{id}/chats` | `?limit=50&offset=0` | `[{ id, name, isGroup, lastMessage, lastMessageAt, unread }]`, newest first |
| GET | `/sessions/{id}/chats/{chatId}/messages` | `?limit=50&before=<unix-ts>` | Messages, newest first, cursor-paged |
| POST | `/sessions/{id}/messages/send-text` | `{ "chatId": "62812...@s.whatsapp.net" \| "62812...", "text": "hi", "replyTo": "<msgId>"? }` | `{ id, timestamp }` |

Errors use the body `{ "error": "...", "code": "SESSION_NOT_READY" }` with an
appropriate 4xx or 5xx status. `chatId` accepts a bare phone number and is
normalised to `<num>@s.whatsapp.net`. Groups use `...@g.us`.

## 6. Real-time events — `GET /events` (WebSocket)

- Auth: the `X-API-Key` header, or `?api_key=` for browsers.
- `?session=main` filters to one session; without it, all sessions are sent.
- Each frame looks like this:

```json
{ "event": "message.received", "session": "main", "timestamp": 1731830400, "data": { ... } }
```

| Event | Fired by (whatsmeow) |
|---|---|
| `session.status` | Connected / Disconnected / LoggedOut / our own transitions |
| `session.qr` | Each code from the QR channel |
| `message.received` | `*events.Message` with `IsFromMe == false` |
| `message.sent` | Our own send, or `IsFromMe` from another device |
| `message.ack` | `*events.Receipt` (delivered / read / played) |

The hub gives each client a buffered channel (about 256 messages) and drops slow
clients so they can't block the event handler. Ping/pong runs every 30 s.

## 7. Data model (our tables; whatsmeow creates its own `whatsmeow_*` tables)

```sql
sessions (id TEXT PK, jid TEXT, push_name TEXT, status TEXT, created_at INT)
chats    (session_id, jid, name, is_group, last_msg_id, last_msg_preview, last_msg_at INT, unread INT,
          PRIMARY KEY (session_id, jid))
messages (session_id, chat_jid, id, sender_jid, from_me INT, ts INT, type TEXT, text TEXT,
          status TEXT, quoted_id TEXT,
          PRIMARY KEY (session_id, chat_jid, id))
CREATE INDEX messages_chat_ts ON messages(session_id, chat_jid, ts DESC);
```

**Where chats and messages come from.** This is the key design point. WhatsApp has
no "list my chats" request, which is why OpenWA stores them itself for Baileys.
We fill the tables from three sources:
1. `*events.HistorySync` right after pairing. The phone pushes recent chats and
   messages: chat names, last messages, unread counts.
2. Live `*events.Message`: upsert the chat and insert the message.
3. Our own sends.

Chat names come from the history-sync conversation name, the contact store
(`client.Store.Contacts`) and `GetGroupInfo` for groups. They are cached in
`chats.name`.

## 8. Message conversion (v1)

We store text for: conversation, extendedText, and the captions of
image/video/document messages. For other kinds we store `type` with an empty
`text`: `image`, `video`, `audio`, `document`, `sticker`, `location`, `contact`,
`reaction`, `unknown`. Downloading media is out of v1, but the schema leaves room
for a `GET .../media` route later.

## 9. Milestones

| # | Deliverable | Done when |
|---|---|---|
| M1 | Skeleton: go mod, config, slog, SQLite plus migrations, `/health`, API-key middleware, Makefile | `go test ./...` and `curl /health` pass |
| M2 | Sessions: create/list/get, connect, QR (JSON/PNG), pairing code, disconnect, logout, delete, auto-reconnect on boot | Phone links over QR; a restart reconnects without a QR |
| M3 | Hub plus `/events` WebSocket, `session.*` events | `websocat` shows QR and status events |
| M4 | Receive: event handler → store → `message.received` / `message.ack` | A message sent from another phone appears on the WebSocket and in the DB |
| M5 | History sync → `GET /chats`, `GET /chats/{id}/messages` | The chat list matches the phone's recent chats |
| M6 | `send-text` (plus reply), `message.sent` | The message arrives on the phone; it is stored and emitted |
| M7 | Hardening: graceful shutdown, Dockerfile, README with curl examples, unit tests | Image builds; README walkthrough works |

**Testing.** The API depends on a `Manager` interface, so its tests run against a
fake manager. The store tests use a real SQLite file in a temp dir. Message
conversion and chat-id parsing are table-tested, and the hub has its own tests.
Real end-to-end checks need a phone and are done by hand from the README walkthrough.

## 10. Risks and notes

- **Ban risk:** the same as OpenWA's Baileys engine (unofficial protocol). Use a
  dedicated number and don't bulk-message strangers. This goes in the README.
- **Chat list completeness:** it only holds what the history sync delivered plus
  what has arrived since. That covers recent chats, not every chat ever.
- **LID addressing:** WhatsApp now sometimes identifies people by `@lid` instead of
  a phone JID. We store whatever JID arrives and map LIDs to phone numbers with
  whatsmeow's LID store where one is known.
- **Protocol churn:** keep `whatsmeow` updated (`go get -u go.mau.fi/whatsmeow`).
  It changes often.
- **Licence:** whatsmeow is MPL-2.0. That's fine for linking, but changes to its
  own files must stay MPL.

## 11. Open questions (defaults in bold)

1. Sessions: **several by id, matching OpenWA** or exactly one?
2. Real-time delivery: **WebSocket only in v1**, or webhooks (HTTP POST to a URL) as well?
3. Viewing a chat: **text and captions only in v1**, or media download too?
