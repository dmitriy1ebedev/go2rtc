# S3-02: bounded sound activity and reconnect behavior

Source: internal/webospip/sound.go. This is an opt-in go2rtc module
using the already configured local RTSP stream. It does not write PCM,
record audio, expose sample bytes or add another streaming registry.

## State machine

SoundMonitor receives 16-bit mono PCM frames from an ffmpeg stdout
pipe, each frame containing 1600 samples at 8 kHz. It computes RMS
dBFS in memory, then exposes only JSON { "state": "..." }.

- active: a sample exceeds the configured dBFS threshold, and the
  configured hold interval has not expired.
- quiet: a recent PCM sample exists, but the hold interval expired.
- unavailable: no recent sample has arrived within 3500 milliseconds.
  Silence and a disconnected RTSP source are not interchangeable.

The retry loop accepts context cancellation and bounds each ffmpeg
sampling attempt to 120 seconds. After failed attempts it uses delays
of 6, 12, 24, 48 and at most 60 seconds; after a successful sampling
session it resets to the 3-second baseline. Exit and error paths
terminate/reap the child process, so missing ffmpeg must not create a
rapid respawn loop. The read-only activity handler shares the exact
webOS file-origin allowlist with the catalog and emits no secret data.

## Verification

- TestSoundRetryDelayBounded verifies every backoff boundary.
- TestSoundMonitorStopsOnCancelledContext checks cancellation without
  starting a real camera or child process.
- TestSoundActivityStatesWithoutFFmpeg covers
  unavailable -> active -> quiet -> unavailable, plus the silence RMS
  floor and half-scale PCM.
- TestCameraAndActivityWebOSOrigins checks that the activity handler
  preserves the installed LG client's Origin contract.
- Final source must pass the exact GitHub draft-PR Go tests, vet and
  production Go build. Tests do not count as real-camera or soak tests.

Remaining S3-02 acceptance requires isolated active/quiet/disconnect
capture with measured FFmpeg CPU/RAM, bounded reconnect and no
subscription leaks. Running go2rtc, TV and Home Assistant remain
unchanged until a separate deployment gate.
