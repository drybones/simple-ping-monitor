# CLAUDE.md

## What this is and who it's for

`pingmon` watches the network connection at a church during the Sunday live stream (about 90 minutes). A Mevo Start camera streams straight to YouTube over RTMP; an Apple Silicon MacBook starts the stream and runs pingmon alongside it. The laptop and the camera share the same wireless access point, so the laptop's view of the network is a good proxy for the camera's. The connection runs partly over powerline adapters, which are suspected of buffering. The symptoms pingmon exists to catch are:

- latency that sits at 20–30 ms, then jumps into the hundreds or thousands of ms;
- duplicate ICMP replies;
- complete dropouts.

The key diagnostic is pinging **the router as well as the internet host**. If both spike together, the problem is inside the building (Wi-Fi or powerline); if only the internet host spikes, it's the broadband line or beyond. Incidents carry `gateway_also` for exactly this reason. Keep that comparison front and centre in any change.

## Constraints and settled decisions

- **One self-contained binary**, with the web UI embedded (`go:embed`). The church Mac must need nothing installed. **No CDN or external assets**: the page has to work when the internet is down, which is exactly when someone will be looking at it. Charts are hand-drawn on `<canvas>`.
- **No `sudo`.** It uses unprivileged ICMP datagram sockets (`udp4`), which macOS allows, and falls back to raw sockets only where those fail.
- **Localhost only.** It binds `127.0.0.1`; viewing from a phone or another device was explicitly not wanted.
- **Defaults**: pings the router (auto-detected) plus `internet=8.8.8.8`, once a second. A ping is *lost* after 5 s with no reply and *late* if the reply arrives after that. Latency classes are good < 60 ms, fair < 150 ms, high < 1000 ms, severe ≥ 1000 ms. The live view offers 30 s / 90 s / 5 min / 15 min spans, defaulting to 90 s.
- **Out of scope** (decided, don't add unasked): alerts or sounds (it runs during a service), OBS integration (no OBS in use), bandwidth tests, a native Mac app, remote dashboards.
- **Deferred**: week-on-week comparison of sessions. Every log ends with a `summary` line so this can be built later without re-analysing.

## Layout

- `main.go`: CLI (the `run` and `view` subcommands, flags, wiring). `terminal.go`: live status line, incident lines and the exit summary.
- `internal/probe`: produces raw `Sent` and `Reply` events and does no interpretation. `icmp.go` is the real pinger (a per-source random token plus a 64-bit sequence in the payload; replies are matched on the token, not the ICMP ID, because Linux rewrites IDs and macOS delivers every echo reply to every socket). `gateway.go` finds the default route. `sim.go` fakes a flaky powerline link.
- `internal/monitor`: turns events into `Sample`s (pending → ok / lost / late, plus a dup count) in `monitor.go`; the pure analysis (stats, status judgement, incident grouping, router correlation) is in `analysis.go`. Put new analysis logic here, in Go, and unit-test it. The browser only renders.
- `internal/logfile`: JSON Lines. There's a `session` header, then a `sample` line each time a ping's state settles or changes (the last line per target and sequence wins), then a `summary` line on clean exit. Reading tolerates a truncated final line. Bump `Version` for incompatible changes.
- `internal/server`: `/api/snapshot` (everything) plus `/api/events` (SSE: `sample` and once-a-second `summary`). A replay session serves the snapshot only.
- `web/static`: `index.html`, `style.css`, `app.js` (vanilla JS, no build step).

## Working on it

```sh
go vet ./... && go test ./...
go run . -simulate -sim-backfill 75m -no-open -log-dir /tmp/pm   # UI with an hour of fake history
go run . view /tmp/pm/<file>.jsonl                               # replay mode
GOOS=darwin GOARCH=arm64 go build -o pingmon-darwin-arm64 .      # the target platform
```

- Check UI changes by running `-simulate` and taking a screenshot with headless Chromium (Playwright), in light mode, dark mode and at narrow width. `docs/screenshot.png` in the README comes from `-simulate -sim-backfill 75m` at 1200 px wide and 2× scale, cropped to the cards and both charts. Refresh it when the UI changes visibly.
- Colours are CSS tokens on `:root` with dark-mode overrides. Latency classes use the status palette (good / fair / high / severe); router and other secondary targets use categorical series colours; duplicates are violet. Don't reuse a status colour for a series.
- Real ICMP can't be exercised against 8.8.8.8 from a sandbox, and there's no Mac there. Test against `127.0.0.1`. On Linux the unprivileged path needs `sysctl net.ipv4.ping_group_range="0 2147483647"`; otherwise it falls back to raw sockets as root.
- CI (`.github/workflows/build.yml`) vets, tests, and builds `darwin/arm64` and `darwin/amd64` tarballs as artifacts. Pushing a `v*` tag publishes a GitHub Release. Keep the actions on Node 24 versions.
