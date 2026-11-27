# Go-native test and IPK packaging migration (23 November 2026)

## Default quality gate (source-only; no deployment)

The H96 MCPshka `.mcpshka/profile.json` **test** workflow uses `go test`
directly with `-p 1 -count=1 -timeout 4m`. It covers
`internal/cameracaps`, `internal/camerametrics`, `internal/webospip`,
`pkg/onvif`, `www`, `webos/ipk` and
`webos/tests`. Standalone `internal/streams`, `pkg/core`, `pkg/rtsp` and `pkg/webrtc`
tests remain on the separately pinned upstream merge-rehearsal CI job:
standalone branches contain inherited symbol/fixture mismatches, while the
merged tree checks those packages with `go test` and `go vet`.
Do not count a skipped standalone package as a passing standalone test.
The independent **build** workflow remains `go build -p 1 -buildvcs=false ./...`.
The Go integration test launches **Node**, not Python, for genuine JavaScript
syntax and webOS client compatibility. It is not a Go interpreter for JavaScript.

The native `webos/ipk` package validates app/manifest versions, builds
deterministic Debian ar + tar.gz packages with fixed metadata, rejects
corrupt archives, unsafe paths/permissions, unexpected members, embedded
camera credentials and forbidden video decoder code. Tests verify reproducibility,
timestamp sensitivity, tampering, application identity and packaging secrecy.
Run `go run ./webos/cmd/ipk --check` without writing an artifact;
`go run ./webos/cmd/ipk` creates the staged output with the normal artifact
path. The repository's webOS CI uses Go for tests, vet and packaging.

## Compatibility boundary and remaining migration work

Python `scripts/test_webos.py`, `webos/build_ipk.py` and
`webos/tests/test_*.py` have **not been deleted**. They are legacy references,
but are no longer used by the main Go-native test, lint or packaging profiles.
Their former 81-test suite included LG provisioning, SSH pinning, downgrade
approval, standalone sidecar upgrades and rollback. Native Go tests do **not**
yet reproduce every negative integration scenario from those 81 tests.
Port that behavior to native Go mock servers and release contract tests
before claiming complete parity; don't silently treat absence of Python
execution as equivalent coverage. Production LG installation is still gated.

**Package digest changed:** the historical Python staging IPK 0.1.18 was
recorded with SHA-256
`c230331423482804d50fee9bdd109e5aa122c928e20e956c34041815b367c639`;
the new Go-built staging IPK from the same version metadata has SHA-256
`b69fcc3ba59a8ab2ec37799a23af46b0809cf1b3a90d45db66ad048ac7655dfb`.
The new artifact is structurally valid and deterministic, but **not
byte-identical**. Never silently replace the published/installed 0.1.18
package. A future deployment needs an explicit version/release change,
re-checked signatures/hashes and guarded TV acceptance.

The Go checks use no real cameras, do not reconfigure Home Assistant,
go2rtc, Lane or LG, and do not authorize any deployment. CI success is
different from a real on-device acceptance.
