# S0-03 — upstream comparison and safe staging integration

Observation date: 2026-11-23. This file records observed source facts,
not a production release or an automatic request to merge upstream.

## Sources and provenance

- Official upstream GitHub: AlexxIT/go2rtc (default master).
  Current observed master: e61f312747b222da561110c5001e7fcd02aa630d.
- Previous observed upstream: 640dc368d8c1ecbea8ac4aed9a4fc1f6d08e9f9a,
  compared with GitHub REST compare API.
- Owner fork: dmitriy1ebedev/go2rtc, confirmed fork=true with
  parent.full_name=AlexxIT/go2rtc; GitHub master observed at
  1d10ccc947d1cf50eebdb96a8a5d62bcb8587868.
- Camera staging branch is feat-camera-server-integration,
  draft PR #2, observed at 94df5c15bea036157f0c05615065078218728257
  when this review began. PR has not been merged or deployed.
- Local /root/repos/go2rtc main was d9f370a7232f8478a9d06c7c1fb95b8eab7f5079,
  clean at the start of this review. Local main and GitHub PR head are
  separate commit identities and must not be treated as the same artifact.
- Latest upstream GitHub release at observation: v1.9.15
  (published 2026-10-25). A release tag and master SHA are distinct.

## Diff matrix and risks

GitHub compare 640dc368..e61f312: 24 commits and 18 changed paths.
Their principal effects:

| Area | Upstream paths | Integration risk and checks |
|---|---|---|
| keyframe requests | internal/streams/add_consumer.go, producer.go | new-consumer timing, stream reuse, first-frame delay; no second stream registry |
| RTSP | pkg/rtsp/conn.go, producer.go, keyframe.go, keyframe_rtcp_test.go and related tests | RTCP PLI/FIR, RTP keyframe handling, setup/replay/play; replay and initial-frame regression |
| WebRTC | pkg/webrtc/server.go, keyframe_test.go | RTCP error reset and keyframe scheduling, multi-consumer stability |
| core | pkg/core/node.go, core_test.go, node_test.go | link consistency and codec/session expectations |
| test baseline | internal/ffmpeg/ffmpeg_test.go, pkg/creds/secrets_test.go, pkg/hap/camera/accessory_test.go, pkg/hap/tlv8/tlv8_test.go | previously known failures may now be corrected; old baseline outcomes cannot be projected onto this newer upstream |
| documentation | internal/rtsp/README.md | confirm public RTSP options before config changes |

GitHub compare fork master 1d10ccc..upstream e61f312 reports
DIVERGED with ahead_by=21 and behind_by=4 (relative to their shared
merge base). The reported merge base was
2ad0481ee5add0f9574a54a37e2c741cdeaef0a2.
This is not a conflict-free merge verdict.

The new 18 upstream paths do not directly overlap the 20 paths
changed by draft PR #2 relative to fork master, based on the two
read-only comparison lists. However, fork master has its own commits
and modified RTSP/core files, so both textual merge conflicts and
runtime semantic regressions remain possible.

## Staging experiment and rollback protocol

1. Keep local main, fork master, the deployed server, webOS and Home
   Assistant unchanged.
2. The GitHub Actions workflow
   .github/workflows/upstream-merge-rehearsal.yml checks out the
   current draft camera branch on a disposable runner, fetches
   pinned upstream e61f312, then attempts git merge --no-commit
   --no-ff. It records unmerged *paths* only if Git reports conflicts.
3. If merge succeeds, run targeted camera, ONVIF, streams, core, RTSP,
   WebRTC, web/client tests, vet and a complete production Go build
   on that merged tree. Every result is explicit, and failures cause
   the CI job to fail. There is no upload of camera credentials.
4. A CI pass proves only textual integration, tests and build on the
   throwaway runner; it does not benchmark first frame, CPU/RAM,
   RTSP reconnect or confirm a real-LG outcome.
5. No merge commit is created or pushed. Rollback of the experiment
   is discarding the runner; the previously verified PR SHA remains
   the safe checkpoint. Any later production integration needs a
   separate protected branch, source/CI identity and deployment
   approval with verified backup and rollback.

Current rehearsal acceptance: PENDING until the exact workflow run
is observed with its commit SHA and final CI verdict. Do not mark
ROADMAP S0-03 closed based on this document alone.
