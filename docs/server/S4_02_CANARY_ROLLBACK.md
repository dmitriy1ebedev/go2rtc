# S4-02  Isolated canary and rollback acceptance

This is a test-only fixture. It never replaces a running go2rtc service,
uses real camera credentials, changes Home Assistant, or installs a release.
The test owns a temporary directory, a UNIX API socket and a local HTTP JPEG
source. Only its own child process is stopped or killed.

## Reproduce

Build a known baseline from an explicitly identified clean source revision.
Verify the artifact with SHA-256. In a separate development checkout create
the ignored file `.tmp/s4-canary-input.json`:

```json
{
  "baseline": "/absolute/path/to/verified/test-binary",
  "baseline_sha256": "<64 lowercase hex characters>",
  "baseline_commit": "<40 lowercase hex characters>",
  "soak_seconds": 300
}
```

The operator must verify the baseline provenance before supplying the
manifest. A hash checks bytes, not the truth of the stated source revision.
Run the named integration test through the registered project test runner,
package `./internal/camerabench`, run expression
`^TestGo2RTCCanaryRollbackAcceptance$`. Outside MCPshka, the equivalent
test command is `go test -count=1 -run '^TestGo2RTCCanaryRollbackAcceptance$' ./internal/camerabench`,
with an appropriate enclosing timeout. The ordinary regression suite does
not execute this opt-in test.

The test builds a temporary candidate, serves a synthetic 4 fps JPEG stream,
checks source marker order, measured latency, one source connection, active
subscriber count and cleanup. It verifies the baseline before the change,
then the candidate, then the restored baseline. Rollback is a checksum-pinned
atomic copy inside the private directory. The report contains child process
CPU time and peak RSS, not host-wide resource use. Latency is synthetic
source-to-JPEG-decode latency, not capture-to-TV-display latency. The fixture
does not measure physical RTSP/Wi-Fi reconnect performance.

Optional manifest field `fault` selects `frame_stall` (watchdog after
5 seconds without progress) or `startup_failure` (an inert, checksum-valid,
non-executable fixture). A normal run injects a candidate crash after the
specified soak. Unexpected playback or startup failure triggers the same
isolated rollback path, but must still fail the test. The expected fault
modes require confirmation of their specific trigger and restored playback.
Metrics not available when the candidate did not start or did not finish
a sample are not evidence of zero latency or zero CPU consumption.

Reports are created under the ignored `.tmp` directory. No report contains
a live camera address or credential.

## Verified development evidence, 2026-11-27

- 300.56 s, 1201 frames, 4.00 fps, 59 health samples, one source;
  mean source-to-JPEG-decode 253.3 ms, max 623.0 ms;
  candidate peak RSS 18140 KiB, CPU 924.276 ms total;
  checksum-verified rollback 4.22 s. Job mwylb565-20, source 88b3829a.
- Frame stall: watchdog triggered, rollback 4.16 s, zero remaining
  consumers/transcoders. Main integration job mwylk4xj-23, source 0c370abb.
- Startup failure: inert candidate rejected, rollback 10.66 s.
  Worktree job mwylj6ty-22, source eaaf4b4b; identical test source
  integrated into 0c370abb.

These are intermediate local results, not full S4-02 acceptance.
The server roadmap requires a continuous 24-hour canary under authorized
load; the normal MCPshka project_test wall-clock limit is 21600 seconds.
Do not replace a continuous run with summed independent jobs or mark the
24-hour criterion PASS from these shorter tests. A separately authorized
durable isolated runner with sufficient time budget and resource limits is
required for the full check. Production remains outside the test scope.
