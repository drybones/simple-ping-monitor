# pingmon

A small command-line tool for watching a flaky network connection during a live stream. It pings your router and an internet host once a second, records every result, and shows a live dashboard in your browser: the last 30 seconds to 15 minutes in detail, the whole session at a glance, and a list of incidents (latency spikes, packet loss, outages, duplicate replies).

It is a single self-contained binary with the web page built in, so it needs no internet access to display and nothing else installed. See [docs/PROPOSAL.md](docs/PROPOSAL.md) for the background.

## Install (Apple Silicon Mac)

Download `pingmon-darwin-arm64.tar.gz` from the latest build (GitHub → Actions → *build* → the newest run → Artifacts, or a Release if one has been tagged), then in Terminal:

```sh
cd ~/Downloads
tar xzf pingmon-darwin-arm64.tar.gz
xattr -d com.apple.quarantine pingmon 2>/dev/null   # tell Gatekeeper it's fine
mkdir -p ~/bin && mv pingmon ~/bin/
```

(Or build it yourself with Go installed: `go build -o pingmon .`)

## Use

```sh
~/bin/pingmon
```

That pings the router (found automatically) and 8.8.8.8, opens http://localhost:8080 and runs until you press **Ctrl-C**, at which point it prints a summary of the session. The terminal also shows a one-line live status and prints each incident as it ends.

Every session is logged to `~/pingmon-logs/pingmon-YYYY-MM-DD_HHMMSS.jsonl`. To look at one again later:

```sh
~/bin/pingmon view ~/pingmon-logs/pingmon-2026-10-04_103012.jsonl
```

Useful options (`pingmon -h` lists them all):

| Option | Default | |
|---|---|---|
| `-target NAME=HOST` | `internet=8.8.8.8` | Host to ping; repeat for more (replaces the default). E.g. `-target internet=8.8.8.8 -target mevo=192.168.1.40` |
| `-no-gateway` | | Don't ping the router |
| `-interval` | `1s` | Time between pings |
| `-loss-after` | `5s` | A ping with no reply after this long counts as lost; if the reply turns up later it's counted as *late* |
| `-good`, `-warn`, `-severe` | `60`, `150`, `1000` | Latency thresholds in ms |
| `-port` | `8080` | Web UI port (tries the next few if taken) |
| `-log-dir` | `~/pingmon-logs` | Where session logs go |
| `-simulate` | | Fake a flaky network, to try the UI out. Add `-sim-backfill 60m` to start with an hour of history |

## Reading the dashboard

**Status cards** judge each target on the last 30 seconds: *Good*, *Degraded* (any loss, duplicates, or 95th-percentile latency over the warning threshold) or *Bad* (3+ lost, p95 over the severe threshold, or no reply for 3 s).

**Detail chart**: one bar per ping to the internet host on a log scale, coloured by latency class, with the router drawn as a blue line. Lost pings are a red column plus a ✕ in that target's row at the top; late replies are hatched with a ○; duplicate replies are purple dots in the *dup* row. A grey bar growing at the right edge is a ping still waiting for its reply. Hover for exact values.

**Whole session**: the session in blocks of a few seconds, showing the median (solid) and worst (faint) latency per block, with loss and duplicates in the rows above. Click or drag on it to zoom the detail chart; *Back to live* returns.

**Incidents**: runs of slow, lost or late pings (short gaps of up to 3 good pings are merged). Each is labelled *outage* (3+ consecutive lost/late), *packet loss*, *severe latency* or *high latency*. For the internet target it also says whether the **router was affected at the same time**: if it was, the problem is inside the building (Wi-Fi, powerline adapters, switch); if the router was fine, it's the broadband line or beyond.

## What it measures, and what it doesn't

pingmon measures the path from *the machine it runs on*. If the camera streams directly (e.g. a Mevo on its own Wi-Fi or Ethernet connection), the laptop's readings only reflect the camera's experience if both share the same route to the router. To watch the camera's own link, add it as a target (`-target mevo=<camera IP>`); spikes to the camera but not to the router point at the camera's connection.

Ping also doesn't measure upload bandwidth. A link can answer pings promptly while being too slow to carry the stream, though in practice buffering adapters usually show up as the latency spikes this tool is designed to catch.

## How it works

Written in Go, with no dependencies beyond `golang.org/x/net`. Each target gets an ICMP socket (an unprivileged datagram socket, so no `sudo` on macOS). Every request carries a random token and a 64-bit sequence number in its payload, so replies are matched exactly: a second reply to the same request is a duplicate, and a reply after `-loss-after` turns an earlier *lost* into *late*. The browser UI is plain HTML, CSS and canvas, embedded in the binary and fed by a JSON snapshot plus Server-Sent Events.

The log is JSON Lines: a `session` header, then one `sample` line each time a ping's state is settled or changes (the last line for a given target and sequence wins), and a `summary` line on a clean exit.

```sh
go test ./...                                              # run the tests
GOOS=darwin GOARCH=arm64 go build -o pingmon-darwin-arm64 .  # cross-compile for Apple Silicon
```
