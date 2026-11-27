#!/usr/bin/env bash
# CI fixture dependency only. Use signed distro packages, never an unpinned
# third-party FFmpeg download. Keep mirror failures visible and bounded.
set -euo pipefail
if ! command -v ffmpeg >/dev/null 2>&1; then
  timeout 90s sudo apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=20 -o Acquire::https::Timeout=20 update
  # A slow signed Ubuntu mirror can take over five minutes for FFmpeg dependencies.
  timeout 600s sudo apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=20 -o Acquire::https::Timeout=20 install -y --no-install-recommends ffmpeg
fi
ffmpeg -version
encoders="$(mktemp)"
trap 'rm -f "$encoders"' EXIT
ffmpeg -hide_banner -encoders > "$encoders"
for codec in libx264 mjpeg pcm_alaw pcm_mulaw aac; do
  grep -E "^[[:space:]]+[A-Z.]+[[:space:]]+${codec}([[:space:]]|$)" "$encoders"
done
