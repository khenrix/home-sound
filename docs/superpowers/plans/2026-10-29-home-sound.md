# Home Sound Implementation Plan

> For agentic workers: use superpowers:subagent-driven-development task-by-task. A fresh implementer and independent reviewer gate each task. Use checkboxes for progress.

**Goal:** Build the approved Go audio replacement, proving microphone, input and output capabilities before production cutover.

**Architecture:** Go owns the canonical sample timeline, sessions, volume and recovery. Narrow external/native adapters handle device-specific protocols; Hall has a thin JS receiver. Each stage is useful independently and hardware gates may block dependent stages.

**Tech Stack:** Go 1.27.3; standard library first; ALSA capture adapter; pinned airplay-cli and go-librespot helpers; compatible Sendspin JS/Go slice; optional focused media resolver/decoder helpers.

**Spec:** docs/superpowers/specs/2026-10-29-home-sound-design.md

## Global Constraints

- Canonical audio is 48 kHz stereo float32 with integer sample positions; maximum output queue is three seconds.
- Group names are TV + All Speakers and All Speakers. Individual destinations remain native.
- Start tests at LG native volume 10; TV native 15 and its measured acoustic level are the strict testing ceiling.
- Capture is explicit, bounded and cancellable; secrets, private state and recordings never enter Git or logs.
- Acoustic sync target <=10 ms with uncertainty <=2 ms; level/change target <=1 dB while unsaturated.
- Roam stays in the office and is excluded from central acoustic calibration.
- No software claim of actual audible playback without acoustic or human evidence.
- No production deployment before the Mac upgrade is verified complete; no Homey uninstall before acceptance and protected restore verification.
- Runtime budget: Go <=64 MiB RSS, total <=256 MiB, CPU <=one logical core on the target i5.
- Use TDD: report observed RED then GREEN, full Go suite and vet before each task completion. No recursive subagents or concurrent implementers.

## Review Focus

- Stale events after recast/reconnect must not affect a newer session (Task 4).
- Hung pipes and cancellation must release processes/devices without orphaning capture (Tasks 2/3).
- Ambiguous/noisy recordings must not produce confident calibration (Task 2).
- A device claiming playing with no sound must remain unverified (Tasks 3/7).
- Manual app changes must not trigger automatic takeover (Tasks 4/6).

### Task 1: Testable audio foundation and CLI

**Files:** go.mod, internal/audio/, internal/wave/, cmd/home-sound/, .github/workflows/ci.yml, README.md, LICENSE.

**Interfaces:** Produce audio.Format (rate/channels/sample format), audio.Frame (Epoch uint64, Sample uint64, Data []float32), canonical 48k stereo validation, PCM16 conversion, WAV read/write helpers, deterministic quiet reference generation. Frame data is immutable while consumed. Expose CLI version, signal, inspect; return errors/nonzero exit for invalid input. No live controls or fabricated device status.

- [ ] Write failing table-driven tests with hand-derived values for PCM16 clipping/quantization, frame alignment/nonfinite samples, sample-count-derived timestamps without cumulative rounding, truncated/unsupported WAV input and CLI errors.
- [ ] Observe RED using /home/khenrix/git/.tools/go/bin/go test ./...; record relevant failures.
- [ ] Implement minimal audio/WAV behavior and CLI. signal writes a bounded deterministic reference WAV, defaults to -40 dBFS, never plays it; explicit duration and output required. inspect reports actual format/duration.
- [ ] Add Go 1.27.3 CI, README with truthful capability/gate status and Apache-2.0 license. No generated tests for documentation/constants alone.
- [ ] Run go test -race ./..., go vet ./..., build and exercise CLI against a generated WAV. Commit and report RED/GREEN evidence.

### Task 2: Explicit capture and trustworthy acoustic analysis

**Files:** internal/capture/, internal/measure/, cmd/home-sound/.

**Interfaces:** Consume Task 1 WAV/format. Produce Capture interface with context cancellation, read frame/sample counters and capture status. Initially wrap one persistent arecord ALSA process (not a shell), explicitly hw:CARD=PCH,DEV=0 or user-selected device. Produce analysis report with reference/recording rate, correlation lag, relative level, noise/clipping/confidence and unverified absolute SPL. CLI record and analyze commands; recordings max60s, default5s, restrictive file mode.

- [ ] Test missing device/helper, nonzero exit, truncated PCM, bounded duration, cancellation and child cleanup using real controlled subprocess fixtures. Test independently constructed shifted/scaled signals with drift, silence/noise/clipping rejection and ambiguous peaks.
- [ ] Observe RED, implement capture and analysis. Add signal -kind probe producing deterministic broadband reference suitable for unambiguous correlation; the Task1 sine is for basic audio only. Use cross-correlation with sufficient efficiency for real recordings; reject uncertainty >2ms and level comparisons with inadequate SNR rather than return a confident mapping. Keep no background mic listener.
- [ ] Full suite/race/vet plus CLI capture fixture. Commit/report. Controller separately verifies actual Mac ALSA access/format and source attribution; no claim of hardware pass from fixtures.

### Task 3: Direct AirPlay output and native LG control

**Files:** internal/airplay/, internal/webos/, internal/discovery/, cmd/home-sound/.

**Interfaces:** Consume canonical frames. Produce Output adapter with Prepare/Write/Start/Stop, readiness/acknowledged anchors, health and metadata. Discover AirPlay/Cast/LG without waking devices. Pair through protected interactive setup; never log credentials. cliairplay persistent stdin plus command pipe, native LG volume through local webOS control. Start default muted/quiet; reject explicit TV volumes >15 in calibration mode.

- [ ] Read current canonical helper source and pin commit/checksum; verify CLI contracts before adapting. Test using controlled helper/server fixtures: corrected anchors, clock not ready, stale epoch, blocked stdin, process exit/cancel, pairing redaction and native-volume limits.
- [ ] Observe RED, implement adapters and focused diagnostic/play commands. Use sample-derived anchors and actual acknowledgements. No shell interpolation of addresses, paths or metadata.
- [ ] Full suite/race/vet, build Linuxamd64 helpers and core. Controller disables only owned Homey audio before hardware tests, pairs safely, sets LG10 before playback, tests LG then Kök. Ask for physical placement/approval only when needed. If single-output audio is unproved, do not proceed to combined playback.

### Task 4: Group ownership, calibration mapping and service

**Files:** internal/session/, internal/volume/, internal/config/, internal/control/, cmd/home-sound/.

**Interfaces:** Consume Source/Output/Capture boundaries. Produce session generation ownership, exactly two configured group memberships, local CLI/control status/events/calibration interfaces and atomic versioned JSON profiles. Active outputs have bounded independent queues and readiness, clock/underrun/error evidence; audible remains unverified unless measured. Monotonic curves map common relative dB targets to native steps plus attenuation; room trims separate.

- [ ] Test source/group switch releases TV, stale callbacks ignored, ten-second one-output loss does not interrupt healthy outputs, bounded retry/rejoin, slow output cannot grow memory, orderly shutdown; test malformed profiles, saturation, return from saturation, equal-dB changes and TV feedback-loop suppression.
- [ ] Observe RED then implement minimal coordinator/recovery/service using injected monotonic clock for simulation. Never use scheduler ticker as audio clock; handle wall-clock jumps in anchor mapping.
- [ ] Run full checks and controller TV/Kök central acoustic gate using Mac capture. Save uncertainty/raw evidence privately. Do not transplant old engine timing offsets as calibrated new values.

### Task 5: Actual Spotify and native-button YouTube input

**Files:** internal/source/, internal/spotify/, internal/youtube/, cmd/home-sound/, docs/input-support.md.

**Interfaces:** Produce Source streams, format/metadata/transport events for the same Task 4 bus. Spotify uses pinned go-librespot external-volume/PCM pipe; two identities, single overlapping session owner. YouTube DIAL/Lounge discovery/control and media resolution are distinct testable boundaries. Use permissively licensed references; do not copy noncommercial or unknown-license implementations. A narrow resolver/decoder helper is allowed; no credentials/cookies or protected-content bypass.

- [ ] Inspect canonical source. Test discovery/launch/stop payloads, play/pause/seek/queue/volume, reconnect, resolver failures, source EOF/cancel, PCM sample continuity and metadata generation with controlled protocol fixtures.
- [ ] Observe RED, implement inputs and controller isolated input probe. Before routing YouTube to speakers, prove actual Android native-button discovery of both names and sustained decoded PCM, including restart/reconnect. Mark unsupported content/app cases explicitly.
- [ ] Full checks; verify Spotify real playback and controls; no universal Cast compatibility claim. Failure of YouTube native input blocks declaring that input supported, not unrelated microphone work.

### Task 6: Hall receiver, screens, wake and recovery

**Files:** receiver/, internal/sendspin/, internal/cast/, internal/wake/.

**Interfaces:** Match Sendspin JS7282880e1bba873e927080c98482246060515ce6/specf417b498e294d34c2c788918724443b6898f972f after source verification. Minimum Hall slice: player/metadata roles, supported controller operations, pairing identity lifecycle, four-timestamp sync, 48k stereo s16 PCM20ms frames, buffer/clock feedback and clean metadata artwork. Direct LG AirPlay remains audio output. Native LG wake/status plus optional existing Chromecast CEC actuator.

- [ ] Test exact matched wire/handshake, timestamp mapping, re-anchor/restart, finite queues and metadata; receiver JS tests cover clean UI, stale metadata and manual foreign-app ownership. Never assert full Sendspin conformance for this slice.
- [ ] Observe RED then implement. Registration/login/payment is human-assisted; prepare concrete receiver first. Initial same-origin LAN hosting is unpublished to registered Hall; prove device acceptance and persistent identity before continuing.
- [ ] Controller verifies Hall sound/screens, then TV/Kök/Hall central sync and one-metre volume curves below ceiling; respects Roam exclusion. Verify native LG actual track/artwork. If LG rendering fails, stop that claim and report before adding another TV receiver.

### Task 7: Deployment, required mic investigation and cutover

**Files:** deploy/, docs/operations.md, docs/capabilities.md.

**Interfaces:** Native systemd release install/rollback, protected state, health/resource report and explicit capability evidence matrix. Required endpoint-mic work uses official source/APK/binary evidence and, if a route exists, actual PCM source attribution/reconnect proof; no firmware modification.

- [ ] Exercise deployment preparation/validation and rollback in a temporary root; test process lifecycle and private-state preservation. Add targeted failing regression tests for any live failures before fixes.
- [ ] Deploy only after upgrade completion and single-controller cutover readiness. Measure RSS/CPU and bounded queues under real playback. Execute 30min stability/sync checks, ten-second recovery, three standby wakes<=30s and30min QuickStartOff deep standby<=90s. Restore intended TV settings.
- [ ] Investigate built-in mic routes with REA where source alone insufficient. Record actual proof or exact blocker; do not conflate mic flags/voice text with recording. No automatic phone fallback.
- [ ] Only after acceptance and protected restore verification uninstall owned Homey audio/probe and exact owned temporary resources. Otherwise retain disabled app/data and report blocked cleanup. Restore Samsung Auto Blocker with user assistance. Complete final whole-branch independent review and publish only truthful verified capabilities.
