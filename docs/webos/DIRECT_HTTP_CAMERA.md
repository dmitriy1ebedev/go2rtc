# Direct HTTP camera mode (experimental webOS 0.1.19)

Development source: canonical GitHub fork `dmitriy1ebedev/go2rtc`, feature
branch `feat-webos-direct-http-camera`. This feature is **not** included
in the owner's accepted installed 0.1.18, the current GitVerse mirror,
or any public release until separately approved on a test LG.

## Usage

Install a **tested** IPK and enter either:
- go2rtc server base URL, such as `http://10.33.44.10:1984`. The
  existing camera catalog, audio, optional native PTZ and sound-activity
  functionality remain unchanged.
- A direct camera JPEG snapshot endpoint, e.g.
  `http://10.33.44.56/snapshot.jpg`. The TV reloads one `<img>` with
  an anti-cache query key at the configured interval.
- A direct multipart MJPEG endpoint, e.g.
  `http://10.33.44.55:8080/video.mjpg` or
  `http://10.33.44.55/mjpeg`. The TV opens one continuous `<img>`
  connection and retries errors with bounded backoff; there is no
  invented `/api/frame.jpeg` fallback if the camera exposes none.

The server/endpoint address is stored in application-local storage.
A direct URL must be a private RFC1918 **IPv4 literal**, HTTP/HTTPS,
with a known `.jpg`, `.jpeg`, `/snapshot`, `.mjpg`, `.mjpeg` or
`/mjpeg` path. This initial release deliberately does not accept
username/password, query-string tokens, arbitrary DNS hosts, redirects,
RTSP, HLS, H264 or H265 URL input. A camera requiring authentication,
an unusual CGI URL, or RTSP should be connected through ordinary go2rtc
instead. HTTPS cameras with self-signed certificates may not load in
older LG webOS browsers.

In direct mode the client does not call go2rtc `/api/streams`,
`/api/webos-pip` or FFmpeg. There is no remote PTZ or sound activity
endpoint and no additional streaming process. The existing viewer's
window size, crop, position, visibility and manual close stay available.

## Known limitations / release gate

- Only the developer regression harness and synthetic URLs are tested;
  cross-origin image decoding on the owner's **LG 55NANO926PB/webOS 6.0**
  and on other webOS versions is NOT yet verified for this feature.
- Some snapshot servers reject additional cache-busting query parameters.
  Such endpoints need go2rtc or a dedicated compatible adapter.
- MJPEG multipart liveness cannot be measured per frame reliably by
  old webOS WebKit. The viewer handles startup/error and retries but a
  silently frozen connection may not raise `onerror`.
- No credentials are embedded in IPK, URLs, diagnostics or GitHub tests.
  `http://10.33.44.x` in this document is synthetic example data,
  not a preconfigured customer's camera.
- Before publishing to GitVerse: prove Node/Python/IPK and GitHub CI
  at an exact commit, inspect the generated IPK/checksum, verify
  clean install, JPEG and MJPEG image load on test TV, confirm
  no regression to the accepted go2rtc mode, and preserve rollback.
