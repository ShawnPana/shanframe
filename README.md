# shanframe

One app per device. Sign in, and every machine is on your list — open its
terminal or its desktop from any other device, anywhere. Built for you *and*
for your agents: everything you can do by hand, an agent can do from this CLI.

This repository is the **shanframe agent and CLI** — the program you install
on your machines. It is open source (Apache-2.0) so you can read exactly what
runs on your computers. The hosted service at https://shanframe.com
(accounts, rendezvous, relay) is what makes it work out of the box.

## Install

```sh
curl -fsSL https://shanframe.com/install.sh | sh
```

or build from source: `go build ./cmd/shanframe`, then `shanframe join`.

## Use

```sh
shanframe ls [--json]              # your machines and what each offers
shanframe <dev>                    # live terminal (name prefixes work)
shanframe <dev> run -- <cmd>       # run a command: stdout/stderr/exit code
shanframe <dev> tunnel 5432        # its port, local here (--socks, --install)
shanframe <dev> cdp                # its Chrome DevTools, local here
shanframe <dev> screenshot|click|type|key|batch   # see and operate its screen
```

`skills/shanframe/SKILL.md` is the ready-made skill for coding agents.

## How it connects

The agent keeps one outbound connection to the rendezvous server. Sessions
run directly between your devices over WebRTC, end-to-end encrypted; the
server only introduces them. Nothing on your machine listens on the internet.

Issues and PRs welcome. The server side is not in this repository.
