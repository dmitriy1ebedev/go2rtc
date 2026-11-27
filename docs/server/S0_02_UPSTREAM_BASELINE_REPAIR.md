# S0-02: inherited RTSP URL parsing and WebRTC vet baseline

Date: 2026-11-23. These changes are tested only on disposable CI
runners. No production, camera, TV, HA or networking is modified.

## Reproduction before repair

GitHub draft PR #2 source e49d2a1 and pinned upstream master
e61f312747b222da561110c5001e7fcd02aa630d were merged by
git merge --no-commit --no-ff on an ephemeral Linux runner.
Run: https://github.com/dmitriy1ebedev/go2rtc/actions/runs/45102974023

- Merge: PASS with no textual conflicts.
- Camera capabilities, webOS-PiP, ONVIF, streams and core packages: PASS.
- Production go build -p 1 -buildvcs=false ./...: PASS.
- webOS JavaScript syntax: PASS.
- pkg/rtsp TestURLParse: FAIL after 6m timeout; recursive urlParse
  at pkg/rtsp/helpers.go repeated an extra ':' for malformed IPv6
  Content-Base and never terminated.
- go vet ./pkg/webrtc: FAIL on four unkeyed pion/webrtc.RTCPFeedback
  literals and one unreachable return after panic.
- Therefore final exact run: FAIL, not a successful integration gate.

Original upstream e61f312 has the identical recursive
pkg/rtsp/helpers.go and the identical unkeyed WebRTC literals.
These issues are inherited source findings, not caused by the
added /api/cameras/capabilities handler. A source comparison alone
is not an independently executed clean-upstream Go test receipt.

## Repair scope and compatibility policy

- Canonicalize a valid unbracketed IPv4-mapped IPv6 RTSP host to
  a bracketed IPv6 URL before url.Parse. Update the test expectation
  to the valid bracketed host; URL.Hostname() remains the same IP.
- Retain a single bounded legacy 'after host' retry. Never recurse
  with an ever-growing address. Malformed authority fails closed.
- Add TestURLParseMalformedHostFailsClosed for malformed host/port.
- Use keyed RTCPFeedback fields; do not alter codec negotiation
  values or their order. Remove unreachable return after panic.
- Lower the isolated integration rehearsal test timeout to 2m.
  Any failed test/vet/build still makes the job fail; no exclusions.

Rollback of these source changes is a Git revert of the dedicated
fork commit. No release or installed binary has been altered.
Verify exact GitHub PR SHA via full targeted tests, vet, build and
the pinned upstream merge rehearsal. Independent isolated camera
and LG acceptance are separate later gates.
