# shanframe on Android phones

An Android phone on the device list is a full target: terminal, commands,
tunnels, **and its screen + touch** — no app, no root. Read this when a task
involves a phone.

## What you can do

Same verbs as a Mac; the phone is just portrait and touch-shaped.

```sh
shanframe <phone> screenshot shot.png      # device pixels, e.g. 720x1600 portrait
shanframe <phone> size                     # current orientation
shanframe <phone> click X Y                # tap
shanframe <phone> click X Y --right        # Back button
shanframe <phone> drag X1 Y1 X2 Y2         # swipe (fast drag = fling)
shanframe <phone> scroll X Y DY            # wheel → list scroll at that spot
shanframe <phone> type text here           # into the focused field
shanframe <phone> key esc                  # Back;  key enter / tab / up / down… also key back / home / recents / power / wakeup
shanframe <phone> run -- getprop ro.product.model     # a shell on the phone (Termux user)
shanframe <phone> run -- termux-battery-status
shanframe <phone> tunnel 8080              # the phone's localhost:8080 here
shanframe <phone> tunnel --socks 1080      # exit through the phone's network (its Wi‑Fi/LTE)
shanframe <phone> batch <<'STEPS'
click 360 1550
sleep 0.5
screenshot home.png
STEPS
```

- Coordinates are pixels of the screenshot (1:1 with `click`). The home pill is
  near the bottom centre (e.g. `click 360 1550` on a 720×1600 phone); the
  Back/Home/Recents soft keys sit in the bottom bar when present.
- **Labels / accessibility tree:** `run --shell -- uiautomator dump /data/local/tmp/ui.xml; cat /data/local/tmp/ui.xml`
  gives every on-screen node with `text`, `content-desc` and `bounds="[x1,y1][x2,y2]"`;
  tap the centre with `click X Y`. Otherwise read the screenshot. Loop
  screenshot/dump → act → screenshot.
- **`run --shell -- <cmd>`** runs as the phone's `shell` user — what `adb shell`
  would run, without adb, on any network: `input tap/swipe/text/keyevent`,
  `screencap -p`, `uiautomator dump`, `am start -n pkg/.Activity`, `pm list
  packages`, `settings get/put`, `dumpsys`, `wm size`, `cmd wifi status`.
  Wake the screen: `run --shell -- input keyevent 224`; lock screen → the
  preview is black (secure surface) until the phone is unlocked — unlocking
  is the user's (ask; never guess a PIN).
- In the app: Desktop tab = live screen; tap/drag/type; right‑click = Back,
  middle‑click = Home. Terminal tab = Termux shell.
- `run` executes as the Termux user: Termux packages (`pkg`), `termux-api`
  tools if installed, and Android's own `getprop`, `am`, `pm`, `input`,
  `settings` *where Android allows app users to run them* (most read-only
  queries work; anything needing `shell` privileges does not — use the screen
  verbs for UI actions).
- Not available: pinch / multi-touch, audio, long-press as a verb (hold via
  `drag` to the same point works for many apps).

## Setup on a phone (once)

1. Install **Termux** from F‑Droid or GitHub (not the Play Store build).
2. In Termux: `curl -fsSL https://app.shanframe.com/install.sh | sh` → approve
   the link code from a signed‑in device. Terminal/commands/tunnels work now.
3. For the screen: Settings → Developer options → **Wireless debugging** on →
   **Pair device with pairing code**, then in Termux `shanframe pair <code>`.
   Keep the dialog open until it says paired. Tick "Always allow on this
   network" when Android asks.
4. Install the **Termux:Boot** add‑on so the agent starts after a reboot.

## After a reboot

Terminal comes back by itself (with Termux:Boot). The screen needs one
switch: turn **Wireless debugging on while on an allowed Wi‑Fi**; within a
minute the device list shows the screen again, and it then works on **any**
network (LTE included) until the next reboot. Pairing never needs redoing.
`shanframe ls --json` → `note` says exactly which step is missing.

## How it works (one paragraph)

Android lets only the `shell` user read the screen and inject input — the
identity adb gets. The agent pairs with the phone's *own* Wireless debugging
over loopback and uses it once per boot to start a helper (the same binary)
as that user, detached. From then on the agent talks to the helper locally:
it runs scrcpy's server (embedded, Apache‑2.0) for H.264 capture and touch
injection, and `screencap` for stills. Frames ride the same WebRTC track the
Mac uses, so the viewer and CLI didn't change.

## Troubleshooting

- `Desktop isn't available…` with a note → follow the note (pairing code /
  Wireless debugging).
- Online but no screen right after a reboot → Wireless debugging is off;
  flip it on Wi‑Fi once.
- Taps land wrong after rotating → take a fresh `screenshot` (size changes).
- Phone sleeping → the agent holds a wake lock while it runs; if the screen
  is off, `click` the power key area won't help — wake it physically or
  `run -- termux-wake-lock`.
