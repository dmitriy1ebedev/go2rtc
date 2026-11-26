# Camera server fork: compatibility and safe staging guide

Status: development candidate, not an installed or published release.
GitHub PR #2 is the review source. Keep the accepted LG client 0.1.18
and the existing server unchanged until separate device acceptance.
Fork release 0.1.0, IPK candidate 0.1.19, and catalog schema v1
are independent version axes. See versions.json and
../webos/DISTRIBUTION_POLICY.md.

## API contract

All camera identities are existing go2rtc stream names, never a second
camera registry. Enable webos_pip.enabled to register the optional
catalog and capability routes. Enable camera_metrics.enabled separately
for structural runtime counters.

| Method and path | Contract |
| --- | --- |
| GET /api/webos-pip | Stable LG v1 adapter; camera stream/label, optional MJPEG and AAC aliases, refresh/poll intervals, legacy PTZ and sound flags. |
| GET /api/cameras/capabilities | version v1, cameras (id, label, streams, features, health). Health unknown until measured; not a camera probe. |
| GET /api/cameras/metrics | version v1, sampled_at, configured producer slots, subscribers, pending subscriptions; optional live HTTP MJPEG mailbox counters. Rejects nonempty Origin. |
| GET /api/webos-pip/activity | Optional active, quiet, unavailable state. No audio samples or recordings. |
| POST /api/webos-pip/ptz | Legacy fixed-camera endpoint; no query string. |
| POST /api/cameras/ptz?camera=NAME | Per-camera motor policy; exactly one camera parameter, application/json, X-Go2RTC-PTZ token. Body exactly one direction field (up/down/left/right/stop). |
| GET /api/stream.mjpeg?src=NAME | Shared stream through existing go2rtc registry; use preconfigured MJPEG alias for H264 conversion. |
| GET /api/frame.jpeg?src=NAME | Single JPEG; cache and snapshot conversion have separate resource limits. |

Configured aliases: NAME_pip_mjpeg, NAME_pip_max_mjpeg, NAME_tv_aac.
Alias capability is advertised only if that stream exists. Catalog is
limited to 64 physical stream identifiers. Audio playback and sound
activity are different features. Sound activity monitors one configured
sound_stream, not every camera. The stock go2rtc client fallback does not
provide these optional fork-specific motor or activity endpoints.

## Safe example: isolated server, no production camera

Use a new private working directory and a new config. Before starting,
verify ports are free, and never reuse the production data directory,
service name, socket, or listening ports. Example below binds only
loopback and has an empty, synthetic publisher slot:

```yaml
app:
  modules: [api, rtsp, ffmpeg, mjpeg, webos_pip, camera_metrics]
api:
  listen: "127.0.0.1:11984"
rtsp:
  listen: "127.0.0.1:18554"
streams:
  demo: null
  demo_pip_mjpeg: "ffmpeg:demo#video=mjpeg"
webos_pip:
  enabled: true
  refresh_ms: 240
  catalog_poll_ms: 15000
  sound_enabled: false
  sound_native: false
camera_metrics:
  enabled: true
```

Use the exact checked source revision and verified artifact for the host
architecture. Extract the CI artifact into a new directory and run
sha256sum -c SHA256SUMS there. Confirm SOURCE_COMMIT matches the reviewed
GitHub revision; review the linked-module SPDX inventory. ZIP archive
metadata need not match across replicas, whereas binary and SPDX bytes
must match. Do not treat an Actions artifact as a signed public release.
The inventory covers linked Go modules only; FFmpeg, OS dependencies
and their licenses require separate review.

Start the isolated binary with -c pointing to the new YAML. Fetch
/api/cameras/capabilities and /api/cameras/metrics on 127.0.0.1:11984.
The catalog should contain demo and advertise the MJPEG alias; absent
publisher does not mean the camera is healthy. For a full video and
sound probe use the automated camerabench fixtures, which start their
own isolated process, publish synthetic data, verify frames and clean
up. Stop only the isolated child process; delete its private temporary
directory after confirming it is no longer in use. Do not stop or
restart an existing go2rtc instance.

## Production migration checklist (separate approval)

- Record old binary checksum, source version, private configuration,
  service identity and rollback command; verify that the saved artifact
  and configuration can actually be read. Do not commit configuration
  containing camera URLs or credentials.
- Verify independent amd64/arm64 build receipts and exact-source
  regression CI, plus the required native sealed test.
- Verify per-camera policy in isolation; each enabled PTZ camera must
  have a distinct random token at least 32 characters long. The
  optional legacy ptz policy binds a fixed camera; ptz_cameras holds
  additional policies (max 32). Only existing named physical streams
  are accepted. Camera source resolution is from configured trusted
  RTSP/ONVIF URLs with credentials and pinned private IPv4; loopback
  restream and ambiguous sources cannot be used as ONVIF identities.
  Configure the actual ONVIF source separately through the supported
  registry; do not let a client supply a destination.
- Use TLS/trusted network isolation and global API authentication.
  Origin checks do not authenticate clients, and "null"/file origins
  are not trust boundaries. Never expose administration API, stream
  credentials, or PTZ tokens to the public Internet. Avoid trace logs
  for credential-bearing requests.
- Native sound mode is opt-in (sound_native: true); requires mono
  PCMA/PCMU at 8000 Hz. Unsupported audio is unavailable; no automatic
  FFmpeg fallback in native mode. Legacy mode remains default.
- On the actual device, measure playback, CPU/RSS, source sessions,
  first frame, recovery, and background film continuity before/after.
  Validate motor stop on the actual camera under supervision. A failed
  network Stop is best-effort, not a physical safety guarantee.
- Only after acceptance switch the separately approved service.
  On failed health or regression restore the prior binary/config and
  verify the old service and TV; no downgrade without approval.
  Neither Home Assistant nor the main HomeKit restream is a migration
  target of this guide.

## Candidate changelog / verified scope

- Universal capability and runtime-count API; stable legacy LG adapter.
- Bounded JPEG cache, per-client latest-frame HTTP MJPEG mailbox,
  write deadline, snapshot FFmpeg admission/time/output limits.
- RTSP publisher interruption recovery and short/long GOP comparative
  fixtures. Aggressive short probe is not a global default: long GOP
  with AAC showed a regression; conservative comparison passes.
- Per-camera PTZ isolation, bounded Stop wait, SOAP acknowledgement
  and pinned redirect rules; actual camera acceptance remains open.
- Native G.711 activity: synthetic PCMA to PCMU republish recovered,
  no remaining subscriptions and no sampled server FFmpeg child.
- Four independent Linux builds and SPDX/checksum comparison at
  GitHub CI 45772881073; sound/relay CI 45772881010.
  Historical experimental failures are retained, not hidden.
- No measured physical TV compatibility or 24-hour canary is claimed
  by these CI results. No release, production migration or HA change
  is implied. Check the current ROADMAP and acceptance receipts.

Attribution: upstream AlexxIT/go2rtc (MIT), historical webOS adaptation
recorded in webos/NOTICE. GitHub is primary; GitVerse export is
webOS-client-only, per the distribution policy.
