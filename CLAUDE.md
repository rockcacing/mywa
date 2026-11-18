# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

mywa is a self-hosted WhatsApp HTTP API in Go: one binary, one SQLite file, no browser. It runs on
[whatsmeow](https://github.com/tulir/whatsmeow) (WhatsApp's multi-device protocol) and borrows its
REST/event shape from OpenWA. `README.md` is the user-facing API reference (routes, event names,
message object, session statuses) and `PLAN.md` holds the original design and milestones. When you
add or change a route, event or field, update the README tables too.

## Commands

```sh
MYWA_API_KEY=<16+ chars> go run ./cmd/mywa   # serves on :8080; the web UI is at /
make build                                   # CGO_ENABLED=0 go build -o bin/mywa ./cmd/mywa
make test                                    # go test -race ./...  (-race needs cgo / a C compiler)
make vet                                     # go vet ./...
go test ./internal/store -run TestArchivedAndPinned -v   # one test
```

Config is env-only (`internal/config`): `MYWA_API_KEY` (required), `MYWA_ADDR`, `MYWA_DB_PATH`
(default `mywa.db`), `MYWA_MEDIA_DIR` (default `media/` next to the DB), `MYWA_LOG_LEVEL`.
The DB file holds the WhatsApp login credentials, so never commit or share `*.db`.

Keep whatsmeow current (`go get -u go.mau.fi/whatsmeow && go mod tidy`): an outdated client shows up
as session status `failed` with an "outdated" error.

## Architecture

Request/event flow: `api` → `wa.Manager` → `wa.Session` (wraps one `*whatsmeow.Client`) → `store`
(SQLite) and `hub` (WebSocket fan-out).

- **`cmd/mywa`**: wiring and graceful shutdown. `Manager.Restore` reconnects sessions that were
  `ready`/`connecting` when the process stopped; `Manager.Close` disconnects without changing the
  saved status so they come back next start.
- **`internal/wa`**: all WhatsApp logic.
  - `Manager` owns `map[id]*Session`. Its public methods look up the session and delegate.
  - `session.go`: lifecycle (connect, QR loop, pairing code, disconnect, logout), sending text,
    revoke. Status is kept in memory and persisted to the `sessions` table.
  - `handler.go`: `handleEvent` is registered per client and called **synchronously from whatsmeow's
    receive loop**, so slow work (history sync, group-name lookups) must run in goroutines. Every
    event is first checked with `isCurrent(cli)`; events from a replaced or disconnected client are
    dropped. Keep that guard on new event handling.
  - `convert.go` (`Extract`): `waE2E.Message` → message type/text/quoted id. `media.go`: download and
    cache attachments, and ask the phone to re-send old media (`ReloadMedia`). `sendmedia.go`: upload.
    `chatactions.go`: archive/pin through app state.
  - Errors are sentinel values (`ErrNotFound`, `ErrInvalid`, `ErrNotReady`, `ErrState`, `ErrUpstream`,
    media errors…). Wrap them with `fmt.Errorf("%w: ...")`. `api/respond.go` `fail()` maps them to
    HTTP status plus a `code`; a new sentinel needs a case there, otherwise it surfaces as 500 `INTERNAL`.
- **`internal/store`**: owns only the `sessions`, `chats` and `messages` tables. whatsmeow's
  `sqlstore` keeps its `whatsmeow_*` tables in the **same DB file** (one shared `*sql.DB`).
  Schema is `CREATE TABLE IF NOT EXISTS` in `store.go`; a column added later must also go in the
  `addColumn` migration list in `Open`, or existing databases won't get it. Message status only
  moves forward (`sent → delivered → read → played`).
- **`internal/hub`**: non-blocking pub/sub. Each subscriber has a 256-event buffer; a full one is
  dropped (closed) rather than blocking the WhatsApp event loop. Event name constants live here.
- **`internal/api`**: stdlib `net/http` with Go 1.22 method/path patterns (`router.go`). Middleware
  order: recover → request log → API-key auth (`X-API-Key`, or `?api_key=` for the `/events`
  WebSocket; `/`, `/health` and `/favicon.ico` are public). The API depends on the `Manager`
  **interface** in `router.go`, not on `*wa.Manager`: adding a manager method means adding it to that
  interface and to `fakeManager` in `api_test.go`.
- **`internal/api/web/index.html`**: the whole browser UI: one embedded file (`//go:embed`) with
  inline vanilla JS/CSS and no build step. It talks only to the REST API and `/events`. `web.go` sets a
  strict CSP (`default-src 'none'`, images/media only from `blob:`), so the page must fetch media
  with the API key and display it through blob URLs, never by direct `<img src>` to the API.

### Domain points that span files

- **WhatsApp has no "list chats" call.** The chat list is built from the post-link `HistorySync`,
  live messages and our own sends (`store.SaveMessage` upserts the chat). `live=false` (history)
  inserts without publishing events.
- **LID addressing:** people can appear as `…@lid`. `Session.canonicalChat` maps to the phone JID
  (`…@s.whatsapp.net`) when known, so one person's messages land in one chat. Use it for any new
  code that derives a chat or sender JID.
- `chatId` in the API accepts a bare phone number or a full JID (`@s.whatsapp.net`, `@g.us`, `@lid`).
- Media messages store the protobuf of the media part (`messages.media`) so the file can be
  downloaded later; files are cached under `MYWA_MEDIA_DIR`. Old rows without it are filled in by
  `FillMedia` when the phone re-sends them.

## Testing

- `api` tests run the real handler against `fakeManager` via `httptest`.
- `store` tests use a real SQLite file in a temp dir.
- `wa` has table tests for conversion only; anything touching a live WhatsApp connection is checked
  by hand with a linked phone (README walkthrough or the web UI).
