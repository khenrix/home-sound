# Group service and input contract

The operator service owns one overlapping session and exposes exactly `TV + All Speakers` and `All Speakers`. The former contains the configured TV and three speakers; the latter contains those same three speakers. A new selection or seek closes the old source, stops its outputs, increments the generation, and clears manual exclusions. Output recovery does not restart healthy members.

This is software integration, not acoustic acceptance. Status reports submitted frames, transport/clock evidence, queue bounds and recovery; audibility stays `unverified`. The pinned adapter does not expose an underrun measurement, which is reported explicitly. No measured household curves or timing offsets ship with this change. The earlier TV-only native-10 observation does not establish a usable acoustic profile. Kök and mixed-output gates remain pending controller work.

## Private configuration

Configuration is version 1 JSON, at most 16 KiB including profiles, in an absolute path inside a real mode-0700 directory. Files are mode 0600. Updates validate before writing, use a synced temporary file and atomic rename, then sync the parent directory. Credential values belong in separate protected pairing files; configuration only references their paths. No driver is contacted by loading configuration or reading status.

A valid **unverified preview** can contain:

```json
{
  "version": 1,
  "groups": {
    "TV + All Speakers": ["tv", "kitchen", "roam", "hall"],
    "All Speakers": ["kitchen", "roam", "hall"]
  },
  "devices": {
    "tv": {"kind": "tv", "adapter": "airplay", "room_trim_db": 0},
    "kitchen": {"kind": "speaker", "adapter": "airplay", "room_trim_db": 0},
    "roam": {"kind": "speaker", "adapter": "airplay", "room_trim_db": 0},
    "hall": {"kind": "speaker", "adapter": "sendspin", "room_trim_db": 0}
  },
  "profiles": {},
  "reference_verified": false
}
```

Zero trims in this preview mean no correction has been applied; they are not measured room calibration. `sendspin` is a reserved configuration identity: its output factory is explicitly unsupported in this task. No Spotify or DIAL identity is advertised.

Real AirPlay configuration additionally supplies `helper`, `helper_sha256`, per-device `host`, `port`, and optional `pairing_file`. A TV also supplies `lg_url`, `lg_pairing_file`, and optional `certificate_sha256`; the native and AirPlay host must match. See `airplay-native.md` for the existing protected pairing process. Pairing never happens automatically in the group service.

A profile is `volume.Profile`: `version`, `verified`, `points` (`native`, `db`), optional declared `native_maximum`, and `provenance` (`adapter`, `format`, `transport`, `sensor`, `native_control`, `reference`). Empty version-1 profiles are allowed only as unverified placeholders. Measured points must be finite and strictly increasing in native step and relative dB. A verified profile's adapter/format/native-control fingerprint must match its configured device (`48000/stereo/pcm16`, and `webos` for the TV). Provenance is operator-supplied evidence, not an automatic acoustic certification.

For calibrated playback, every selected member needs a verified profile and configuration needs a verified `reference_ceiling_db`. The target plus the member's separate room trim is inverted to a measured native step at or above it, with nonpositive digital attenuation. Above the measured top, a member saturates only when that top is also its declared native maximum; otherwise playback is rejected as calibration-limited. Returning below a native maximum resumes tracking. Native feedback interpolates only inside the measured reference span, never outside it. Own acknowledgements and duplicates are suppressed. A native-step change stops and flushes that member before preparing a fresh adapter with the new gain, rejoining the unchanged canonical timeline. This can leave a preparation gap; it does not seek or pause the source. Remote TV observations also trigger this reconciliation when the mapped native step is unchanged. An observation outside the verified ceiling/range restores the last authorized plan and records `native_change_rejected`. Status retains `last_remote_native` as the last unsuppressed observation, not current native readback. Acknowledgements belong to one adapter attempt. If teardown fails, that member stops with `stop_failed` and the engine quarantines the receiver. `quarantined_devices` remains visible after stop, recast, seek and profile/config replacement. Identity includes the configured ID and normalized physical host, so renaming a device or changing its service port cannot clear unresolved teardown. A new selection retries Stop on the original failed output and is rejected unless that operation succeeds; it never treats a fresh generation as teardown evidence. Other receivers remain selectable. There is no force-clear control.

TV playback retains Task 3's native-10 preflight. After Prepare, the requested native TV step is applied and read back before any PCM is released; the TV ceiling remains 15. Speaker percentages can differ from TV levels, but are accepted by the coordinator only within verified profiles and the acoustic ceiling. AirPlay speaker commands have no native readback and are reported as submitted control. A special `tv_calibration` selection admits only the TV at native 1–15 and makes no acoustic claim. It cannot enable other members or use the relative-dB control API.

## Operator commands

```sh
home-sound serve -config /absolute/private/config.json -socket /absolute/private/operator.sock
home-sound status -socket /absolute/private/operator.sock
home-sound status -socket /absolute/private/operator.sock -preview
home-sound profile -socket /absolute/private/operator.sock
home-sound profile -socket /absolute/private/operator.sock -device tv -file /absolute/profile.json
home-sound control -socket /absolute/private/operator.sock -action pause
home-sound control -socket /absolute/private/operator.sock -action resume
home-sound control -socket /absolute/private/operator.sock -action stop
```

The service listens only on the explicit mode-0600 Unix socket in a mode-0700 directory. Existing socket paths are not replaced automatically. SIGINT/SIGTERM closes HTTP and stops the owned session. There is no LAN administration listener.

`control -action select` accepts `-group`, `-input` (canonical PCM16 WAV), and either `-target-db` or TV-only `-tv-calibration`. `-first-sample` creates a new generation starting at that WAV sample, including non-frame-aligned seeks. `-action target -target-db` changes the relative-dB target; `-action mute -muted true|false` stops/rejoins adapters while the track continues. `-action exclude -epoch GENERATION -device ID` preserves an intentional exclusion until a new selection. These are explicit playback controls; configuration preview alone never invokes them.

HTTP routes are `/v1/status`, `/v1/config`, `/v1/events`, `/v1/profiles` (GET), `/v1/profiles` (PUT `{device, profile}` while idle), and `/v1/control` (POST). Command fields are `action`, `group`, `input`, `first_sample`, `target_db`, `tv_calibration`, `muted`, `epoch`, and `device`. Pause/resume/stop need only `action`. Bodies reject unknown fields, trailing JSON and values over 16 KiB. Configuration responses omit helper/device endpoints and private pairing paths. Events are a bounded snapshot of the last 64 transitions, not a persistent log. Profile changes preserve the engine's generation sequence.

The existing `record`/`analyze` CLI remains the capture/measurement boundary. Analysis results and provisional profiles do not automatically set `verified` or authorize a reference ceiling. Live capture/calibration and acoustic acceptance remain separate controller operations.

## Task 5 source interface

`internal/session.Source` is intentionally small:

```go
type Source interface {
    Format() audio.Format
    Metadata() airplay.Metadata
    Read(context.Context) (audio.Frame, error)
    Close() error
}
```

The format must be canonical 48 kHz, two-channel float32. `Read` returns finite, immutable interleaved data, contiguous absolute sample indices, and 1–960 stereo frames. The engine stamps `Epoch` and normalizes arbitrary chunk boundaries onto 20-ms frames; the final frame may be shorter. `io.EOF` ends production and cancels transient failure recovery while healthy submitted audio drains. An explicit native-volume reconfiguration may prepare a fresh output and replay the bounded retained EOF tail at a feasible sample on the unchanged timeline, including when EOF arrives during that preparation. It does not resume source reads or enable background retries after EOF. Any other error aborts the source and remains visible. Read must honor context cancellation; Close is called once, after consumption ends. A source handed to a successful selection belongs to the engine. A rejected selection leaves ownership with its caller.

Metadata/format accessors are immutable and nonblocking. Optional `EventSource.Events() <-chan SourceEvent` supports `Kind:"metadata"` with `Metadata`, and `Kind:"transport"` with `Transport:"paused"`, `"playing"` or `"buffering"`. Paused/playing invoke the coordinator's pause/resume transition; buffering is informational. Metadata fields are bounded to 1024 bytes and reject control-line delimiters. Closing the event channel ends events, not PCM. Source/adapter buffers outside Read must also be bounded. An already executing Read may finish during pause; the engine retains that bounded frame instead of canceling or dropping it.

Pause stops existing adapters before returning, retains source ownership and the canonical media sample, and keeps unplayed PCM. Initial selection and resume prepare fresh adapters before starting the media clock. Resume waits for the first member to finish preparation/native control, then schedules retained PCM one second ahead. Other late members independently recover at a feasible current sample. `paused`, `resuming`, and `position_sample` distinguish frozen preparation from scheduled playback. The frozen sample can fall inside a frame; replay is sliced without skipping those samples. Mute is separate and leaves the media timeline moving, including play while muted. Position is the canonical source position, not a claim about measured audible arrival.

## Output, clock and resource bounds

`session.Factory` receives a device request containing configuration, the session generation, the shared frozen `ClockOffsetNS`, and an attempt-owned native feedback callback. It returns a fresh `session.Output`: the existing `airplay.Output` methods plus `SetNative(ctx, step)`. One instance owns one Prepare/START. All operations must honor context cancellation and Stop must finish bounded teardown. Successful Stop is the boundary before reconfiguring native control; it must discard previously submitted PCM. AirPlay cancellation submits STOP, waits at most 200 ms for its status, then signals SIGTERM for the pinned helper's disconnect/TEARDOWN cleanup. A helper that does not exit is killed after a further two seconds; forced termination, a nonzero exit after PCM submission, or a reported stop/flush failure makes Stop fail. A failed initial connection with no submitted PCM may still recover. Stop itself has a three-second wait bound. Helper shutdown evidence is not a physical acoustic measurement. The real AirPlay wrapper creates native-control connections only on selection.

Linux `CLOCK_MONOTONIC_RAW` supplies elapsed time. `session.Freeze` captures one raw-to-Unix offset for the process and passes it unchanged to every helper; future Sendspin integration must use that same mapping. Source timestamps use rational sample/rate arithmetic. Pause/resume shifts the session's media base, never the global mapping. Wall-clock adjustments therefore do not move audio. Go timers only bound/pause production and recovery; they never create sample counts or determine DAC rate.

The entire source read and normalized frame end stay at most three seconds ahead. Each member queue is at most 150 20-ms chunks (three seconds). Shared replay is at most four seconds, retaining the send-ahead and a frame spanning pause; bounded normalization/in-flight frames are separate (at most two 20-ms frames). Slow members overflow/cancel independently; output writes never block the source dispatcher. Retries use 1/2/4/8/15-second backoff, at most a 120-second failure episode, with 20-second preparation/initial-START bounds. Thirty seconds of sustained transport starts a fresh failure episode on a later loss. EOF, stop, exclusion and intentional transport controls cancel pending recovery. There is no goroutine per audio frame or retry backlog.

Requested and acknowledged anchors remain distinct. Corrected times and actual content-cut samples are retained from the adapter; drain completion re-evaluates acknowledged anchors. NTP transport readiness does not become clock measurement, synchronization or verified audibility. Real acoustic gates, receiver timing corrections and native control behavior still need measurement before household profiles can be marked verified.

## Transport provenance and converter completion

AirPlay devices accept an optional `transport` object with `rate` (44100 or
48000) and `timing` (`ntp` or `ptp`). Defaults remain 48000/NTP. Set Sonos outputs
explicitly to 44100/PTP; this is a transport selection, not a measured room
profile. `AirPlayFactory` passes the selection and the session's existing frozen
clock offset unchanged. See `airplay-native.md` for owned-daemon and FIR limits.

Verified AirPlay profiles now also require `provenance.transport`, the exact
`airplay.Transport.Fingerprint(helperSHA256)` string. It records native realtime
AP2, wire rate/channels/PCM depth, required timing, converter version and helper
hash. `provenance.format` continues to describe the canonical source format.
An old profile without transport provenance, or a changed rate, timing, filter
version or helper hash, is rejected and requires fresh measurement. Do not copy
an old curve and label it verified merely to satisfy the schema. Unverified
previews still work without contacting devices.

Outputs may optionally implement `Finish(context.Context) error`. The member
calls it once after consuming the final canonical frame, including the last
partial frame, and keeps the receiver alive through the acknowledged drain
horizon. Cancellation must interrupt Finish. A failed finish records
`finish_failed` and invokes ordinary Stop; failed receiver teardown still takes
precedence and quarantines the receiver. Intentional native-volume changes after
EOF retain and replay the source tail through a fresh converter. Existing source
and mandatory output interfaces are unchanged.
