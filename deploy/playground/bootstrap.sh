#!/bin/bash
# One-time (per fresh Claude cloud container) setup for the playground: installs Tailscale, builds
# Caddy, mywa and tic-tac-toe-3d. Safe to re-run. Needs outbound access to pkgs.tailscale.com,
# the Go module proxy, npm and github.com (clone). See README.md for the full runbook.
set -euo pipefail
STATE=${PLAYGROUND_STATE:-/home/user/.playground}
REPOS=${PLAYGROUND_REPOS:-/home/user}
TS_VERSION=${TS_VERSION:-1.104.7}
mkdir -p "$STATE/bin" "$STATE/tailscale"

# Tailscale (static binaries; the container has no systemd or TUN device, so userspace networking).
if [ ! -x "$STATE/tailscale/tailscaled" ]; then
  curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${TS_VERSION}_amd64.tgz" | tar -xz -C "$STATE/tailscale" --strip-components=1
fi

# Caddy: GitHub release downloads are blocked here, but the Go module proxy works.
[ -x "$STATE/bin/caddy" ] || GOBIN="$STATE/bin" go install github.com/caddyserver/caddy/v2/cmd/caddy@latest

# mywa
[ -d "$REPOS/mywa" ] || git clone https://github.com/rockcacing/mywa "$REPOS/mywa"
(cd "$REPOS/mywa" && go build -o "$STATE/bin/mywa" ./cmd/mywa)
[ -f "$STATE/apikey" ] || { head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$STATE/apikey"; chmod 600 "$STATE/apikey"; echo "generated API key in $STATE/apikey"; }

# tic-tac-toe-3d, built to live under /tic-tac-toe-3d (the Cloudflare deploy leaves BASE_PATH unset).
[ -d "$REPOS/tic-tac-toe-3d" ] || git clone https://github.com/rockcacing/tic-tac-toe-3d "$REPOS/tic-tac-toe-3d"
(cd "$REPOS/tic-tac-toe-3d" && git fetch -q origin ccr-11c3ceb0-dev0ay && git checkout -q ccr-11c3ceb0-dev0ay \
  && npm install --no-audit --no-fund && BASE_PATH=/tic-tac-toe-3d npx next build \
  && cp -r .next/static .next/standalone/.next/static)

echo "done. Next: log in to Tailscale and start the funnel (README.md, steps 3-4)."
