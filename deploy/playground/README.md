# Playground: several apps behind one Tailscale Funnel in a Claude cloud container

```
internet -> Tailscale Funnel (https://playground.<tailnet>.ts.net, port 443)
         -> Caddy 127.0.0.1:8000
              /                  landing page   (landing/index.html)
              /mywa/             mywa           127.0.0.1:8080  (prefix stripped)
              /tic-tac-toe-3d/   Next.js game   127.0.0.1:3000  (built with BASE_PATH)
```

Everything runs inside the container, which gets suspended or reclaimed at times. Nothing here
survives that except what is in a git repo or in `$PLAYGROUND_STATE` (default `/home/user/.playground`,
which a fresh container does not have). So a new container means: run these steps again.

## What lives where
| Thing | Location | Kept by |
|---|---|---|
| This config, scripts, landing page | `mywa/deploy/playground/` | git |
| mywa code, game code | `rockcacing/mywa`, `rockcacing/tic-tac-toe-3d` | git |
| Tailscale login, WhatsApp login (`mywa.db`), API key, media | `$PLAYGROUND_STATE` | **nothing**: lost with the container |

Losing `$PLAYGROUND_STATE` means a new Tailscale login (and a new device), and re-scanning the
WhatsApp QR. Copy it somewhere safe if you want to avoid that. Never commit it: `apikey`, `tsstate/`
and `mywa.db` are secrets.

## Steps for a new container
1. **Bootstrap** (installs Tailscale, builds Caddy/mywa/game; several minutes):
   `bash /home/user/mywa/deploy/playground/bootstrap.sh`
   Clone `rockcacing/mywa` first (`git clone`) so the script is there. The game is built from branch
   `ccr-11c3ceb0-dev0ay` until the `BASE_PATH` change is on its default branch.
2. **Start the watchdog** as a tracked background task: Bash with `run_in_background: true`,
   `timeout: 7200000`, command `bash /home/user/mywa/deploy/playground/watchdog.sh`.
   It starts tailscaled, mywa, the game and Caddy, and restarts them if they die or the container's
   proxy port changes. The 2-hour timeout is the maximum; start it again when it ends.
3. **Log in to Tailscale** (first time only, once tailscaled is running):
   `S=/home/user/.playground; T="$S/tailscale/tailscale --socket=$S/tailscaled.sock"`
   `$T up --hostname=playground` and open the printed URL. An auth key (`--auth-key`) also works.
   The device name decides the public URL: `playground.<tailnet>.ts.net`.
4. **Publish through Funnel** (once; it persists in `tsstate`):
   `$T funnel --bg 8000`. Needs Funnel enabled for the tailnet (admin console, Access controls).
5. **Check**: `curl -i http://127.0.0.1:8000/mywa/health`, then open
   `https://playground.<tailnet>.ts.net/`. The first request after login can take a minute while
   the HTTPS certificate is issued (see `tailscaled.log`).
6. **mywa login**: open `/mywa/#key=<contents of $PLAYGROUND_STATE/apikey>` and scan the QR.

## Adding an app
Run it on its own local port, add a `handle`/`handle_path` block to `Caddyfile`, add a card to
`landing/index.html`, and add its start line and liveness check to `watchdog.sh`. Use `handle_path`
(strips the prefix) for apps served from `/`; use `handle` for apps built with a base path.
Apps with absolute asset paths (`/api/...`, `/_next/...`) need a base-path setting to work under a prefix.

## Gotchas learned the hard way
- Outbound traffic goes through the agent proxy, and its port can change (`/root/.ccr/README.md`).
  tailscaled and mywa must be restarted with the new `HTTPS_PROXY`; the watchdog does this.
- There is no TUN device or systemd: tailscaled runs with `--tun=userspace-networking`.
- GitHub release downloads are blocked (403); the Go proxy, npm and pkgs.tailscale.com work.
- Next.js renames its process to `next-server`, so check the port, not the command line.
- All apps share one browser origin: one app's JavaScript can read another's localStorage.
- Container restarts kill every process; only a message to the session brings it back.
  For real uptime run this on a machine you control (the same files work with systemd and a TUN-enabled tailscaled).

## Optional: automate step 1-2 with the environment's setup script
Claude cloud environments can run a setup script when a container is built. `bootstrap.sh` can be
that script (add `git clone https://github.com/rockcacing/mywa /home/user/mywa` before it), which
saves the manual bootstrap. It cannot start the watchdog (that has to be a tracked background task
in the session) or log in to Tailscale.
