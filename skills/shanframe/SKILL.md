---
name: shanframe
description: Use the shanframe CLI to reach the user's other machines — run commands on them, forward ports / borrow their network, and see and operate their screens (macOS). Use whenever a task involves another device on the user's shanframe list.
---

# shanframe — your other machines, from here

Every machine the user installed shanframe on is reachable from this one with
the `shanframe` CLI. Same account, no keys to manage, peer-to-peer and
end-to-end encrypted. Everything below works from any device on the list.

## Find machines

```sh
shanframe ls            # human list: name, os, online/asleep/offline, screen
shanframe ls --json     # [{id,name,os,online,asleep,screen,native,services:[…]}]
```
`services` says what each machine offers right now: `shell`, `exec`, `tunnel`,
`screen`, `input`, `screenshot`. Names match by prefix: `shanframe rasp …`
reaches "Raspberry Pi 5". Offline/asleep machines can't be reached; say so
instead of retrying in a loop.

## Run commands (any machine)

```sh
shanframe <dev> run -- <command line>
```
ssh semantics: runs in the user's login shell on that machine, from their home
directory; stdout and stderr come back separately; the exit code is yours.
stdin is piped when it's not a terminal (`cat file | shanframe pi run -- tee x`).
Quote shell syntax so it reaches the remote shell: `run -- 'cd app && make'`.
Prefer `run` to an interactive terminal; `shanframe <dev>` (no verb) opens a
live shell for humans. Everything a `run` starts dies when the command ends
(its whole process group is cleaned up) — to leave something running on the
far machine use the machine's own service manager (`systemctl --user`,
`launchctl`, `tmux new -d`, `setsid`), and for tunnels use `--install`.

## Ports and networks (any machine)

```sh
shanframe <dev> tunnel 5432                # localhost:5432 here → that machine's localhost:5432
shanframe <dev> tunnel 8080:db.internal:80 # a host only that machine can see; resolved there
shanframe <dev> tunnel --socks 1080        # SOCKS5 proxy here; connections leave from that machine
```
The command stays in the foreground while the tunnel is up (run it in the
background, then use it). Add `--install` to keep a tunnel permanently: the
background service listens from then on and connects on first use;
`--uninstall` removes it; `shanframe tunnels` lists them. This is also how
you "host" an app for the whole list: run it on one machine, and on each
machine that should see it, `shanframe <host> tunnel <port> --install` —
`localhost:<port>` there is now that app (verified: a web app on a Mac,
opened in a Raspberry Pi's browser). Use the SOCKS form
with `HTTP_PROXY=socks5h://localhost:1080` (the `h` matters: names resolve on
the far side). Use a plain port forward for things that can't use a proxy —
e.g. Chrome DevTools: `shanframe mac tunnel 9222` then talk to localhost:9222.
TCP only.

## Look and act on a screen (macOS targets today)

Coordinates are points in the screenshot — what you see is what you click.

```sh
shanframe <dev> screenshot shot.png [--json]   # {"file","w","h"}; also `-` for stdout
shanframe <dev> size                            # e.g. 1512x982
shanframe <dev> click X Y [--right|--double]    # tap = click
shanframe <dev> drag X1 Y1 X2 Y2
shanframe <dev> scroll X Y DY                   # negative DY scrolls up; or X Y DX DY
shanframe <dev> type some text here
shanframe <dev> key cmd+space                   # enter, esc, tab, cmd+shift+t, ctrl+c, up/down/left/right
shanframe <dev> batch <<'STEPS'                 # many steps, one connection (fast)
click 640 400
type hello
key enter
sleep 0.5
screenshot after.png
STEPS
```
Loop: screenshot → decide → act → screenshot to verify. One verb is one
session (~0.3–3 s); `batch` is the fast path. Each line of `batch` is a verb
above plus `move X Y` and `sleep SECONDS`. Linux targets have no screen verbs
yet (terminal only from the CLI); `ls --json` tells you.

## Drive another machine's Chrome (CDP through a tunnel)

Works for any Chromium-based browser with remote debugging on. Two ways the
far machine can have it on:
- **Chrome, recent versions:** `chrome://inspect/#remote-debugging` → "Allow
  remote debugging for this browser instance". This is *gated* mode: the
  `/json/*` discovery endpoints answer 404; you connect straight to the
  browser WebSocket whose path is in the `DevToolsActivePort` file.
- **Any Chromium (Brave, Edge, Chromium, Arc…):** launched with
  `--remote-debugging-port=9222 --user-data-dir=<a dir>` — then `/json/version`
  works too.

One verb does the plumbing — finds the browser's debug port on the device
(DevToolsActivePort, or a listening Chromium process), tunnels it to a free
local port, detects gated vs classic, prints what to connect to, and keeps
the tunnel up until Ctrl-C:
```sh
shanframe pi cdp &                 # stdout: CDP_WS=ws://localhost:PORT/devtools/browser/…  (+ CDP_URL=http://… when classic)
shanframe pi cdp --json            # {"device","mode","url","ws","local_port"}
shanframe pi cdp --port 9222       # if it can't find the port itself
```
Then hand `CDP_WS` to any CDP client, e.g. browser-harness:
```sh
eval "$(shanframe pi cdp 2>/dev/null | head -2)" &   # or parse --json
BU_NAME=pi BU_CDP_WS="$CDP_WS" browser-harness <<'PY'
print(page_info()); new_tab("https://example.com")
PY
```
Verified both ways: Mac Chrome in gated mode, and a Raspberry Pi's Chromium
launched with `--remote-debugging-port`. Playwright/Puppeteer take `CDP_URL`
(classic) or `CDP_WS`. Keep `localhost` as the host — Chrome only trusts that.
You're operating the user's real browser with their logins: act on the task,
don't browse around; prefer a new tab, leave theirs alone.

## Rules of the road

- You act as the user, on their machines, with their permissions. Do what was
  asked; don't wander.
- Check `online` first; an asleep Mac comes back when it wakes, not because
  you retry.
- Destructive commands on another machine deserve the same care as here.
