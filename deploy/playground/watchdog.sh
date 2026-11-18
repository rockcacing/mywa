#!/bin/bash
# Keeps tailscaled, mywa, the tic-tac-toe-3d server and Caddy running in a Claude cloud container.
# Run it as a tracked background task (run_in_background, timeout 7200000). See README.md.
#
# tailscaled and mywa reach the internet only through the agent proxy, whose port can change;
# when it does they are restarted with the new address.
set -u
STATE=${PLAYGROUND_STATE:-/home/user/.playground}   # tsstate/, mywa.db, apikey, media/, logs, bin/
REPOS=${PLAYGROUND_REPOS:-/home/user}               # where mywa/ and tic-tac-toe-3d/ are cloned
DEPLOY=$REPOS/mywa/deploy/playground
GAME=$REPOS/tic-tac-toe-3d/.next/standalone
TS_DIR=$STATE/tailscale
SOCK=$STATE/tailscaled.sock

log() { echo "$(date -u +%FT%TZ) $*" >> "$STATE/watchdog.log"; }
current_proxy() { grep -o -m1 'http://127.0.0.1:[0-9]*' /root/.ccr/README.md 2>/dev/null; }
proxy_env() { echo HTTPS_PROXY=$1 https_proxy=$1 HTTP_PROXY=$1 http_proxy=$1; }

start_tailscaled() {
  pkill -TERM -x tailscaled; sleep 2; pkill -KILL -x tailscaled 2>/dev/null
  env $(proxy_env "$1") setsid nohup "$TS_DIR/tailscaled" --tun=userspace-networking \
    --statedir="$STATE/tsstate" --socket="$SOCK" >> "$STATE/tailscaled.log" 2>&1 < /dev/null &
  echo "$1" > "$STATE/.ts_proxy"; log "started tailscaled (proxy $1)"
}

start_mywa() {
  pkill -TERM -f "^$STATE/bin/mywa\$"; sleep 2; pkill -KILL -f "^$STATE/bin/mywa\$" 2>/dev/null
  env $(proxy_env "$1") MYWA_API_KEY="$(cat "$STATE/apikey")" MYWA_DB_PATH="$STATE/mywa.db" \
    MYWA_ADDR=127.0.0.1:8080 setsid nohup "$STATE/bin/mywa" >> "$STATE/mywa.log" 2>&1 < /dev/null &
  echo "$1" > "$STATE/.mywa_proxy"; log "started mywa (proxy $1)"
}

start_game() {
  pkill -f next-server 2>/dev/null; sleep 1   # Next renames its process to "next-server"
  (cd "$GAME" && PORT=3000 HOSTNAME=127.0.0.1 setsid nohup node server.js >> "$STATE/game.log" 2>&1 < /dev/null &)
  log "started tic-tac-toe-3d"
}

start_caddy() {
  pkill -x caddy 2>/dev/null; sleep 1
  XDG_DATA_HOME=$STATE/caddydata XDG_CONFIG_HOME=$STATE/caddyconf PLAYGROUND_LANDING=$DEPLOY/landing \
    setsid nohup "$STATE/bin/caddy" run --config "$DEPLOY/Caddyfile" --adapter caddyfile \
    >> "$STATE/caddy.log" 2>&1 < /dev/null &
  log "started caddy"
}

log "watchdog started"
n=0
while true; do
  [ $((n % 20)) -eq 0 ] && log "heartbeat"   # a gap between heartbeats means the container was suspended
  n=$((n + 1))
  p=$(current_proxy)
  if [ -n "$p" ]; then
    if ! pgrep -x tailscaled >/dev/null || [ "$(cat "$STATE/.ts_proxy" 2>/dev/null)" != "$p" ]; then start_tailscaled "$p"; fi
    if ! pgrep -f "^$STATE/bin/mywa\$" >/dev/null || [ "$(cat "$STATE/.mywa_proxy" 2>/dev/null)" != "$p" ]; then start_mywa "$p"; fi
  fi
  (exec 3<>/dev/tcp/127.0.0.1/3000) 2>/dev/null || start_game
  pgrep -x caddy >/dev/null || start_caddy
  sleep 15
done
