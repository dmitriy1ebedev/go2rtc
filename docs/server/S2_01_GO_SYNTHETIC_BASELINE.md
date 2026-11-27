# S2-01 — reproducible Go synthetic camera baseline (stage A)

Status: **partial**. Neither a live camera nor an installed go2rtc
server was benchmarked. The separate `go2rtc_end_to_end: false` field
is mandatory in the machine-readable result.

## What runs

`go run ./cmd/camera-bench` invokes only a local FFmpeg `lavfi`
`testsrc2` fixture (160x90, configured 8 FPS, 24 MJPEG frames) and two
independent Go HTTP clients over a temporary localhost-only HTTP server.
No RTSP, camera credentials, HA, LG device or production port is used.

`internal/camerabench` records **measured**, not inferred values:
- duration from FFmpeg process start until the first *complete* JPEG
  frame; observed FPS across completed JPEG frames
- FFmpeg process user/system CPU time and peak resident memory via
  `ProcessState.SysUsage` on Linux
- duration from each client GET until its first complete JPEG, observed
  mean FPS for two independent consumers
- Go benchmark process user/system CPU time and peak RSS via `getrusage`

All incoming frames and total bytes are bounded; multipart responses
are decoded by Go, with checks for correct JPEG SOI/EOI bytes. Timeouts
are bounded. Unit tests validate malformed/truncated JPEGs, limits,
two simultaneous clients and honest reporting. There is no continuous
monitor, log of private camera URLs, or additional stream registry.

## Smoke result — H96, not isolated from production

Go CLI output from **2026-11-23 05:55:17 UTC**, 24 synthetic frames,
two clients, command exit 0, no changes to production services:

| Measurement | Observed |
|---|---:|
| FFmpeg first complete JPEG | 1,046.13 ms |
| FFmpeg measured output | 10.05 FPS |
| FFmpeg CPU user / system | 945.18 / 148.00 ms |
| FFmpeg peak RSS | 47,440 KiB |
| HTTP client first-frame median | 128.63 ms |
| HTTP clients measured mean | 8.26 FPS |
| Frames received per client | 24 |
| Go process CPU user / system | 29.49 / 47.12 ms |
| Go process peak RSS | 26,976 KiB |

The source's nominal 8 FPS and FFmpeg's measured 10.05 FPS are
different: the latter is based on timestamps of the **completed
output frames**. Do not substitute configured FPS for observed FPS.

The H96 shares CPU and memory with live workloads, so **these figures
do not establish the S2-01 isolated baseline or real camera latency**.

## Required remaining acceptance

1. Run this exact Go benchmark on an isolated off-box CI runner, pin
   the commit and preserve its JSON receipt and resource figures.
2. Extend the benchmark to include the **actual go2rtc MJPEG/stream
   pipeline**, rather than only FFmpeg and an independent Go HTTP server.
3. Measure first-frame/latency, FPS, CPU/RAM of FFmpeg and go2rtc
   consumers under representative bounded concurrency, reconnect,
   and load; compare client/no-client baseline without credential logs.
4. Record a passing phase checkpoint for each acceptance criterion,
   verify exact-SHA sealed Go test and run `phase_gate S2.1` before
   attempting `roadmap_close`.

This stage is source-only. It does not authorize deployment or release
of the Go-native 0.1.18 IPK whose checksum differs from the previous
Python-built staging package.
