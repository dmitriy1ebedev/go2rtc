# S1: client-neutral camera capabilities (experimental)

Source: `internal/cameracaps` reads a fresh snapshot supplied from the
existing go2rtc `streams` registry. It is not another source registry,
media server, camera scanner, or device-control service.

## HTTP contract

Both endpoints are currently registered **only** when
`webos_pip.enabled: true` is configured:

- `GET /api/webos-pip`: established webOS v1 schema, unchanged. It is
  derived from the same capability snapshot through an adapter.
- `GET /api/cameras/capabilities`: new, experimental, versioned JSON
  for browser, TV, Home Assistant, or other read-only clients.

Example (invented stream identifiers, not a live device response):

```json
{
  "version": "v1",
  "cameras": [
    {
      "id": "door",
      "label": "door",
      "streams": {
        "video": "door",
        "mjpeg": "door_pip_mjpeg",
        "audio": "door_tv_aac"
      },
      "features": {
        "ptz": false,
        "sound_activity": false
      },
      "health": "unknown"
    }
  ]
}
```

Stream alias fields only appear if they already exist in go2rtc; no RTSP
URL, username, password, PTZ token or PCM content is returned. Optional
`mjpeg_max` is a distinct previously configured alias. Maximum 64 physical
cameras, alphabetic ordering, no duplicate IDs. Technical profiles
(`_pip_mjpeg`, `_pip_max_mjpeg`, `_tv_aac`) are not separate cameras.
Cameras and their profiles are resolved per HTTP request; PTZ/sound
readiness is bounded by the server's endpoint registration at startup.

`health: "unknown"` is deliberate. Registered stream existence does not
prove that the device is reachable, that a consumer is connected or that a
frame can be decoded. A measured health probe belongs to later S2 work,
not to an invented `healthy` flag.

## Access policy and compatibility

The two read-only handlers use the same narrow legacy browser-origin
policy shared with webOS PTZ: absent Origin, `null`, `file://`, and the two
exact installed webOS app file origins are accepted. Arbitrary file origins,
web origins and wildcard CORS are rejected. They return
`Cache-Control: no-store`, reject non-GET methods and do not grant motor access.

**Origin is not authentication.** This compatibility policy was inherited
from the installed webOS client and is not suitable as a public internet
security boundary. Keep the API behind the existing trusted server/LAN
access controls until dedicated authentication is designed and tested.
The separate token-protected PTZ route is not changed.

## Test and release requirements

- Test package `./internal/cameracaps`: aliases, feature flags, unknown
  health, sorting, 64-item limit, de-duplication and secret-like names.
- Test package `./internal/webospip`: exact legacy JSON shape, new
  versioned endpoint, origin policy and removal of technical profiles.
- CI must run on the exact commit. No production service restart,
  TV installation or remote device test is implied by these source changes.
- Formal S1-01/02 closure requires the project phase gate and any required
  deployment/canary acceptance; merely merging code does not close it.
