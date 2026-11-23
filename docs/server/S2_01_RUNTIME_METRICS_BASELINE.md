# S2-01 (stage A): client-neutral, opt-in stream-count baseline

Status: source-only staging; does not satisfy the whole S2-01 Exit.
There is no production deployment and no LG or Home Assistant change.

## Opt-in configuration

```yaml
camera_metrics:
  enabled: true
```

The independent `camera_metrics` module runs after `streams.Init`.
If `app.modules` is explicitly restricted, include `camera_metrics`
there too. Without `camera_metrics.enabled: true` no new endpoint is
registered and no collection, background worker, camera connection or
FFmpeg process starts.

Read-only `GET /api/cameras/metrics` serves a JSON snapshot:

```json
{
  "version": "v1",
  "sampled_at": "2026-11-23T03:30:00Z",
  "cameras": [
    {
      "id": "front_door",
      "runtime": {
        "producer_slots": 1,
        "consumers": 2,
        "pending_subscriptions": 0
      }
    }
  ]
}
```

These numbers are **instantaneous structural counters**, not inferred
camera health or throughput:
- `producer_slots`: number of configured producer slots, including
  disconnected entries; NOT the number of active RTSP streams
- `consumers`: subscribers currently registered with that Stream
- `pending_subscriptions`: pending add-consumer operations

All counts come from the existing go2rtc `streams.Stream`: a brief
stream mutex guards producer/consumer slice lengths, and its existing
atomic counter supplies pending count. This is bounded and does not
read unsynchronized RTP packet counters. Discovering camera names
reuses `internal/cameracaps.Discover`: sorted, deduplicated, at most
64 physical cameras; helper aliases are excluded. Deleted streams
are omitted on the next request. No second camera registry is used.

The response does not contain RTSP/ONVIF URLs, IP addresses, source
metadata, credentials, tokens, raw audio, per-user addresses or device
information. For non-browser monitoring, the endpoint inherits
go2rtc's API authentication and network access policy; no separate
token exists. HTTP requests carrying an `Origin` header are forbidden,
and the endpoint emits no CORS allow headers. If API authentication is
disabled, callers with network access may still read camera identifiers
and subscriber counts; restrict the API to a trusted network or enable
authentication. `Cache-Control: no-store` and `nosniff` are set.

## Explicitly not yet measured

- Real media FPS, bitrate, packet drops and per-consumer queue depth
- First decoded/video frame latency
- FFmpeg CPU/RAM
- RTSP reconnect duration and health
- Any physical camera online/offline condition

Those must be measured with bounded, race-safe instrumentation in the
existing core/streaming pipeline and load tests, **not** represented as
zero in this response. The interface is additive so future measured
fields can be introduced without breaking clients. Do not accept the
full S2-01 ROADMAP item based on this first baseline.

## Regression evidence

- `TestSnapshotRuntimeCountsUsesExistingRegistryState` and
  `TestSnapshotRuntimeCountsConcurrentSubscribers`: synchronized
  structural counters, no source starts or RTSP I/O
- `TestMetricsAPIReadOnlySortedAndNoSecrets`: exact schema, sorting,
  alias filtering and no fake unmeasured fields
- `TestMetricsAPIOriginMethodAndLiveStreamChanges`: GET-only,
  cross-origin forbidden, stream deletion reflected
- `TestMetricsAPICapsPhysicalStreamsWithoutCallingUnknown`: 64-camera
  bound and bounded callback calls
- Require GitHub Actions on exact PR SHA: new packages + upstream
  integration tests, Go vet, production Go build and webOS/IPK checks
  PASS. Green CI does not constitute installed/canary acceptance.

Rollback is reverting the new opt-in module and Stream snapshot method.
No existing /api/webos-pip* route or LG app is modified.
