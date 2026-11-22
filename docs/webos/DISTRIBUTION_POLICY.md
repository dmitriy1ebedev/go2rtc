# Distribution: one GitHub project, webOS-only GitVerse export

Updated 2026-11-22. The development source of truth is the real public
GitHub fork https://github.com/dmitriy1ebedev/go2rtc
(GitHub API: fork=true, parent=AlexxIT/go2rtc). It is the place for
upstream sync, server changes, webOS code, tests and future upstream PRs.

GitVerse dmitriy_1ebedev/go2rtc is **a distribution mirror**, not another
project to develop or a second go2rtc streaming server. Change code in GitHub
source only and export the TV-client allowlist from a tested pinned commit.

The publish profile `.mcpshka/publish.json` uses `code_only` with
`exclude=["*"]` and a short `keep` list. GitVerse receives webOS app
HTML+JavaScript, manifest, icons, default config, IPK builder, versions,
license, attribution, client/IPK tests and GitVerse CI; it must receive no Go
server code, source history, LAN settings, SSH materials or account secrets.
CI builds `.ipk`, checks reproducibility, creates SHA256SUMS and uploads the
TV-only bundle. Artifact upload does not equal a public release. The existing
GitVerse repository is private and older snapshots may still be reachable
from its history: never make it public until that history is audited.

On an LG television, the user installs the IPK through LG Developer Mode,
enters their go2rtc URL and picks a camera. Stock go2rtc is a supported
baseline: the client falls back from optional `/api/webos-pip` to
`/api/streams` and uses `/api/frame.jpeg` or `/api/stream.mjpeg`.
Camera-specific source naming, JPEG transcoding, cross-origin restrictions
and network authentication need a documented fresh-install test. Optional
server PTZ and sound-activity API are not assumed on stock go2rtc.

An IP-only camera source **can only be used directly by the browser when it
provides a compatible HTTP JPEG or MJPEG endpoint**. Raw RTSP is not a
directly supported webOS browser image source. Support for direct camera HTTP
URLs is a future separately tested client feature, not a capability delivered
by the existing 0.1.18 IPK.

The owner's accepted 0.1.18 test on LG 55NANO926PB/webOS 6.0 is not evidence
of compatibility on other TVs, apps or firmware. Do not alter the installed
version or start a new production deployment as part of repository cleanup.

Licenses: go2rtc is MIT (Alexey Khit). No Free My TV runtime dependency,
launcher or branding is shipped. The current `webos/NOTICE` still records
historical adaptation from MIT Free My TV sources. Keep relevant attribution
until file-by-file provenance proves it is no longer required; changing
runtime dependencies does not automatically remove copyright obligations.

Release gates: review every historical GitVerse snapshot and privacy; verify
allowlist, package membership, SHA256SUMS, licenses, client tests and source
SHA; test fresh install using a STOCK go2rtc server; only then publish a
public release and installation guide. GitHub remains primary for all code.
