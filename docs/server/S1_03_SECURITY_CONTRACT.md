# S1-03: camera API and PTZ security contract

Scope: source and mock tests only. Nothing is installed on LG or the live
go2rtc instance.

## Access

The opt-in webos_pip module exposes read-only /api/webos-pip and
/api/cameras/capabilities, optional sound activity, and token-protected
POST /api/webos-pip/ptz. The two catalogs inherit existing go2rtc
api.username / api.password / api.local_auth middleware; they have no
separate application token. Therefore Origin/CORS is NOT authentication:
do not expose an unauthenticated go2rtc API to the public internet.

The installed LG app uses absent Origin, null, file://, and two exact
file://io.go2rtc.webos.pip[-webos] variants. Unknown origins fail closed;
no wildcard origins are introduced. PTZ additionally requires a
32-character-or-longer configured secret in X-Go2RTC-PTZ. Do not send
this header over an untrusted plaintext connection.

## Motor request rules

POST accepts application/json with an optional UTF-8 charset. The body
must be <=160 bytes and contain exactly one JSON object with exactly one
lowercase direction string: up, down, left, right, or stop. Duplicate,
unknown or case-variant keys, concatenated JSON, trailing content,
compound directions and unsupported media types fail before any motor
operation. Existing LG JSON.stringify({direction:direction}) is valid.

The PTZ controller serializes and throttles movements; stop bypasses
movement cooldown. ONVIF motor steps are bounded and attempt emergency
Stop on a missing Move acknowledgement. ONVIF SOAP preserves standard
Go HTTP redirects for camera compatibility, following the upstream
maintainer's review of AlexxIT/go2rtc#2732 (closed unmerged). WS-Security
uses PasswordDigest rather than transmitting a plaintext password, but
digest does not encrypt the SOAP body or replace TLS. Select trusted
ONVIF sources and restrict camera access to the private network.
The client never supplies a camera hostname/URL: configured go2rtc
stream sources provide the address, and local DNS must resolve to one
unambiguous private IPv4 address before dialing.

## Evidence and limitations

TestPTZRejectsAmbiguousOrOversizedJSONWithoutMotorCommands checks
negative requests cause zero motor calls and LG Stop still succeeds.
Existing tests cover origin allowlisting, PTZ authentication, cooldown,
private-DNS SSRF protection, capability catalog secrecy and ONVIF
Stop/redirect behavior. All device operations in these tests are mocked.

The final draft-PR commit must pass GitHub targeted Go tests, vet, full
production Go build and webOS regression/IPK CI. A cloud PASS is not
a production deployment, a real-camera test, or a completed roadmap
phase. A caller with network access can still read stream names if
go2rtc's API auth is disabled. Global api.origin="*" is an independent
configuration concern requiring an explicit deployment review.
