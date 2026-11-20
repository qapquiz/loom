# Grounding: herdr-core (hc)

hc 0.1.21 is installed at ~/.local/bin/hc. It is the server this client attaches to. Local only for v1. No machines, relays, or the docs API.

## Connection

The core listens on a Unix socket. Plain mode is JSON-RPC 2.0, one message per line. `hello` with `{"framed": true}` switches to framed mode. Attach requires framed mode. Everything else works in both.

## Frames

Every frame is `[channel u32 LE][kind u8][len u32 LE][payload]`, payload at most 16 MiB.

| kind | name | payload |
|---|---|---|
| 0 | DATA | channel 0 carries JSON-RPC; other channels carry stream bytes |
| 1 | CREDIT | u32 LE, bytes the receiver consumed |
| 2 | CLOSE | from the core, a JSON reason |
| 3 | RESIZE | u16 LE cols, u16 LE rows, viewer to core |
| 4 | FOCUS | 1 gained, 0 lost |
| 5 | FACTS | JSON display facts |
| 6 | AUTOMATIC_INPUT | terminal-generated reply bytes, only after negotiation |

Flow control: a stream starts with 256 KiB of credit. The viewer sends CREDIT for consumed bytes. A viewer more than 4 MiB behind gets a fresh repaint instead of the backlog. A stalled stream never blocks channel 0.

## Attach

`terminal.attach` returns a channel. The core sends the first paint (current screen, optional history, latest status and title, and the CSI 9001 win32-input-mode flag), then the program's raw output minus queries the core already answered. The viewer sends keys as DATA on the same channel and RESIZE, FOCUS, and FACTS as they change. The last active viewer sets the size. Detach is CLOSE; program exit arrives as CLOSE with `{"reason":"exited","exit_code":...}`.

AUTOMATIC_INPUT is for replies the client's emulator generates (device attributes, size reports, XTGETTCAP, kitty graphics replies). It requires `automatic_input` in `hello.features` and confirmation on the attach reply. Old cores ignore frame kind 6, so clients must negotiate before sending it.

## Events

`events.subscribe` returns a snapshot plus notifications. Event types: created, lost, restarted, keep, finished, removed, status, turn, title, labels, size, foreground. Status comes from OSC 7501 reports by the program. A heartbeat arrives every 15 seconds. A subscriber that falls behind gets `missed` with a fresh snapshot; drop events whose seq is not above it. seq restarts per core process.

## CLI for tests and humans

`hc run --bg`, `hc ls`, `hc get <t> --json`, `hc read <t> --lines N`, `hc send`, `hc wait`, `hc attach`. Plain programs have no agent status; `hc wait` on them waits for exit.

## Source of truth

Protocol details: https://raw.githubusercontent.com/herdrdev/herdr-core/master/schema/README.md (read this session). Full method list: `hc schema` prints OpenRPC. Site index: https://herdr.dev/llms.txt.
