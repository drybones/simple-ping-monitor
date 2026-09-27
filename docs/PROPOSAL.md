# Simple Ping Monitor: Proposal

## Problem

During the Sunday live stream the church MacBook's connection (which runs partly over powerline adapters) shows three symptoms: latency that sits at 20–30 ms then jumps into the hundreds or thousands of ms, duplicate ICMP replies, and complete dropouts. The aim is a lightweight tool that runs for the length of a service (~90 minutes), records what the link is doing once per second, and shows both "right now" and "the whole service so far" at a glance.

## Proposed shape

A single command, run from Terminal, that starts pinging and serves a local web page until you press Ctrl-C:

```
pingmon                      # pings 8.8.8.8 and the router, serves http://localhost:8080
pingmon --target 1.1.1.1     # override the internet target
```

**Decided:** by default it pings two targets in parallel: 8.8.8.8 (Google Public DNS) as the internet target, and the default gateway (the router), discovered automatically from the Mac's routing table. Both are configurable.

There is one process and nothing else to install or run. Every sample is appended to a timestamped log file (JSON Lines or CSV) so a service can be reviewed afterwards, and the web UI can load a past log as well as a live session.

## How it measures

The simplest robust approach is to wrap the macOS system `ping` and parse its output, rather than opening raw ICMP sockets. macOS `ping` needs no `sudo`, already reports duplicates (`DUP!`), and prints `icmp_seq` for every reply, which is all we need. Tracking sequence numbers gives each of your cases a precise definition:

| Case | Definition |
|---|---|
| Normal | Reply received, RTT under a "good" threshold (default 60 ms) |
| High latency | RTT over a warning threshold (default 150 ms) or severe threshold (default 1000 ms) |
| Late reply | Reply arrives after its timeout was already reported. This matters here: buffering powerline adapters often deliver multi-second replies that naïve tools count as lost |
| Duplicate | Second or later reply for the same `icmp_seq` |
| Lost | No reply for a sequence within a longer grace window (default 5 s) |
| Outage | Three or more consecutive losses, recorded as a single incident with a start time and duration |

Jitter (the mean absolute difference between consecutive RTTs) is also worth computing, since it matters to streaming more than raw latency does.

Pinging **the default gateway (the router) alongside the internet target** is the key diagnostic. If the gateway ping spikes too, the problem sits between the Mac and the router (i.e. the powerline adapters); if only the internet target spikes, it is the ISP or beyond.

## Web UI

One page, updated live over Server-Sent Events. Router and internet results are drawn on the same axes (e.g. router as a muted line under the internet bars), so you can see at a glance whether a spike appears on both:

1. **Status banner**: Good / Degraded / Bad, based on the last 30 s, with current RTT, loss %, and jitter.
2. **Live strip (last 30–60 s)**: one bar per ping on a log-scale RTT axis (so 20 ms and 2000 ms are both readable), coloured by class, with red ticks for losses and purple markers for duplicates.
3. **Whole-service overview**: the full session compressed into 10 s buckets (≈540 for 90 minutes), each showing min/median/max RTT plus loss and duplicate counts, so a bad patch at 11:20 stands out at a glance. Clicking or brushing a region zooms into it.
4. **Summary and incident log**: totals and percentages per class, p50/p95/p99 RTT, longest outage, and a list of incidents ("11:14:02, 38 s of RTT > 1000 ms, gateway also affected").

## Implementation choice

I'd recommend **Go**: it compiles to a single self-contained binary for Apple Silicon or Intel with the HTML/JS embedded, so the church MacBook needs nothing installed (no Python, Homebrew, or Node). CPU and memory use are negligible alongside OBS. Charts would use a small library such as uPlot, bundled locally so the page works even when the internet is down (which is exactly when you'll be looking at it). Python with `uv` would be a reasonable alternative if you'd rather be able to tweak the code on site.

## Out of scope (for now)

Bandwidth testing, a native Mac app, remote or cloud dashboards, and automatic remediation are all out of scope.

## Questions for you

1. **The Mac.** Is it Apple Silicon or Intel, and which macOS version? Can you run an unsigned binary downloaded from GitHub (a one-off Gatekeeper "Open Anyway" step), or is the machine locked down?
2. **Where you'll watch it.** Only on the streaming Mac, or also from a phone or tablet on the church Wi-Fi? The latter means listening on the LAN rather than just localhost.
3. **Alerts.** Do you want an audible or macOS notification when an outage starts, or is it strictly glance-and-review? (During a live service, sound is probably unwelcome.)
4. **Thresholds.** Do the defaults above (60 / 150 / 1000 ms, 5 s loss window) match what you'd call good, bad and awful for your stream?
5. **After the service.** Is the log file plus the ability to reload it in the UI enough, or would a one-page summary export (e.g. Markdown or PNG) for comparing Sundays be useful, such as before and after replacing the powerline adapters?
6. **Upload throughput.** Ping won't show upload bandwidth collapsing, which is often what actually breaks a stream. Would you like OBS's own dropped-frame stats (via obs-websocket) overlaid later, or keep this purely about ping?
