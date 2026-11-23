# S3-01 — Stop on ambiguous ONVIF motor acknowledgements (staging)

Verified source-only recovery on 2026-11-23. No physical camera was
commanded, and the deployed go2rtc, Home Assistant and LG were untouched.

## Problem and safe behavior

The local `pkg/onvif.Client.PTZStep` inherited an unsafe branch:
when `ContinuousMove` returned a transport/HTTP error, it returned
before issuing `PTZStop`. An ONVIF motor may have begun moving even
when its HTTP acknowledgement was lost; therefore failure is ambiguous.

This source change synchronizes the already-reviewed, passing GitHub
draft PR #2 ONVIF client and mock tests into local `main`:
- Valid movement sends `ContinuousMove`, waits a bounded 100–450 ms,
  then issues `Stop` for the *same verified profile*
- If Move has no acknowledgement, issue a **separate best-effort Stop**
  even when the first call returned a timeout, 5xx or an unexpected
  SOAP response; preserve the Move failure result
- If emergency Stop also fails, return both errors via `errors.Join`,
  not an apparent successful motor state
- ONVIF SOAP client never follows redirects to a different endpoint;
  this also applies to legacy capability discovery requests

The short `ContinuousMove` envelope specifies an ONVIF `PT1S`
timeout, but a nonconforming camera can ignore it; Stop is
**best-effort**, not a guarantee of physical shutdown on network loss.
A live-camera recovery test remains required before S3-01 closure.

## Regression coverage

`pkg/onvif/ptz_test.go` uses localhost-only mocked HTTP endpoints:
- `TestPTZStepSendsShortMoveAndStop`: happy-path ordering
- `TestPTZStepAttemptsStopAfterMissingMoveAck`: Move 502,
  Stop accepted/rejected; errors are not silently swallowed
- `TestPTZStepStopsAfterLostMoveTransportAck`: abruptly close TCP
  before the Move acknowledgement, observe exactly one Stop attempt
- `TestPTZRequestDoesNotFollowCredentialBearingRedirects`:
  all 301/302/307/308 destinations remain uncontacted

CI must pass the exact GitHub draft PR SHA with targeted Go tests,
`go vet`, full production build and pinned upstream merge rehearsal.
This is not an installation or real-camera acceptance.

Separate S2-01 security guard: the new opt-in metrics endpoint also
clears wildcard CORS headers inherited from global `api.origin: "*"`;
GET with cross-origin `Origin` remains forbidden.
