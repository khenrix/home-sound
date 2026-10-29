# Direct AirPlay and LG native control

The Go adapter uses the official [airplay-cli v0.5.9](https://github.com/music-assistant/airplay-cli/tree/0a58c239d5fb3cf8927b9707a71c6fe897266b6a), commit `0a58c239d5fb3cf8927b9707a71c6fe897266b6a`. These commands are for an explicitly authorized single-device hardware proof. The implementation has been exercised with local protocol and subprocess fixtures; audible output and receiver behavior remain hardware gates.

## Helper provenance

Build from that commit with its pinned recursive submodules:

```sh
git submodule update --init --recursive
make HOST=linux PLATFORM=x86_64 STATIC=1 CC=gcc CXX=g++ VERSION=0.5.9
bin/cliairplay-linux-x86_64 --check
sha256sum bin/cliairplay-linux-x86_64
```

The local Linux amd64 build observed on 2026-10-29 reported `cliairplay v0.5.9 check`, with SHA256 `9843e4c2788de6ce8385111babaf916b3361a90779865e4f839574624cc36797`. This is a locally built artifact checksum, not a claim that every toolchain produces identical bytes. Supply the verified checksum of the artifact being used through `-helper-sha256`. The adapter checks it before starting a process.

Pinned source file SHA256 values:

| File | SHA256 |
| --- | --- |
| `src/cliairplay.c` | `f956d534d630efa9f633ed3ed622c6331c5fadb6ecd5ca06a50723b678c20cc8` |
| `src/ap2_client.c` | `d4554f21f62ddcc0b6b22b9aecc628e213f1f63c7bc87b6c82f0d2f530fe525b` |
| `src/ap2_io.c` | `4750726226f0b117c14e35d2280e218cd68a717b7f26338fdb29a8c6b9f583a9` |

The pinned README explicitly states that NTP readiness is unmeasurable and must not be awaited. `ap2cl_clock_readiness` returns a zeroed `cold` report without effective PTP. The adapter accepts this validated `mode=ntp state=cold` report for transport startup while keeping `ClockReady=false` and `ClockMeasurementKnown=false`. `ClockMode` and `ClockState` expose only validated values. PTP still requires measured `state=ready`; cold/probing/stalled states do not bypass the gate. Unknown modes, malformed fields and inconsistent clock reports fail closed.

`cliairplay.c` supplies persistent PCM on stdin, a private command FIFO, `START_UNIX_MS` and `ACTION=START`, metadata fields followed by `ACTION=SENDMETA`, and `ACTION=STOP`. `ap2_io.c` emits normalized status lines on stderr; some diagnostic statuses also use stdout. Both streams are privately parsed and never forwarded. `HelperErrorCode` allowlists the release's known failure codes (otherwise `unknown`); `HelperHTTPStatus` retains only zero or a valid numeric HTTP status. Raw `detail`, credentials and argv are excluded, including key-like strings embedded inside detail. CLI helper-failure messages include only those sanitized code/status fields. `started.at_unix_ms` is the acknowledgement. A later `anchor_corrected` changes the acknowledged instant; its requested content cut stays pending until `content_cut.cut_bytes` reports the actual discarded PCM. `Anchor.Sample` advances by those bytes divided by four for stereo PCM16.

## LG safety policy

LG playback always uses native AirPlay with `--volume 0`. In this release, zero suppresses the initial native volume setter; it is **not a mute operation**. Go never exposes an AirPlay `VOLUME` command. Native webOS is the LG volume owner.

The adapter sets and reads native volume 10 before connecting the helper, repeats set/read after connected and usable transport timing status, and requires an acknowledged native volume subscription before becoming ready. A failed read, mute, mismatch, invalid state or failed subscription prevents PCM and START. During calibration, a subscription failure, mute or volume above 15 stops the helper. Independent native volume reads run every second with a one-second request deadline; a missed deadline, invalid state, mute or level above 15 also stops the helper. This detects a wedged native control service even when its WebSocket stays open, within approximately two seconds plus process teardown and scheduler delay. Subscription events remain an independent fast path while a readback is pending. This controls software ordering; it does not establish an acoustic safety level or prove actual receiver playback.

Normal Stop requests helper `ACTION=STOP`, then bounds teardown and kills an unresponsive child. Failures use immediate child cancellation. Receiver buffering and audible stop latency need hardware observation.

## Pair locally

Use a new private directory below the operator's workspace, with directory mode 0700 and files 0600. Existing permissive files and symlinks are rejected. Pairing JSON binds credentials to the endpoint and retains the same AirPlay DACP identity. Never print pairing files.

```sh
home-sound pair-lg -url ws://TV_IP:3000 -pairing-file /absolute/private/lg.json
home-sound pair-airplay -helper /absolute/cliairplay-linux-x86_64 -helper-sha256 VERIFIED_SHA256 -host TV_IP -port 7000 -pairing-file /absolute/private/airplay-lg.json
```

Accept the LG registration prompt on the TV. Enter the AirPlay screen PIN in the local terminal stdin. The helper's credential response is consumed privately. The official helper accepts stored auth only through `--auth`, so the value is present in the child process argv at runtime; it is never printed or placed in shell command text by this adapter. Pairing is bounded to 90 seconds.

For a TV requiring TLS, use `wss://TV_IP:3001` with a trusted CA, or an independently verified certificate SHA256 via `-certificate-sha256` for LG commands and `-lg-certificate-sha256` for AirPlay commands. A configured pin replaces CA/hostname verification with exact leaf certificate matching. There is no unrestricted insecure-TLS switch and no automatic certificate trust.

The webOS request shapes follow [PyWebOSTV](https://github.com/supersaiyanmode/PyWebOSTV/tree/55cbd0303fd3211c4e898ae1728b934a4ca5d2aa); the unsigned registration manifest follows [LGTV Companion V3](https://github.com/JPersson77/LGTVCompanion/blob/a37d054296d4957d607d8509272ad211d4a44e50/Common/lg_api.h), narrowed to `CONTROL_AUDIO`. The WebSocket dependency is maintained [coder/websocket](https://github.com/coder/websocket), pinned in `go.mod` to v1.8.14. Actual TV permission acceptance remains a hardware gate.

## Read-only inventory and discovery

```json
[
  {"name":"TV","kind":"lg","host":"192.0.2.10","port":3000},
  {"name":"Kök","kind":"airplay","host":"192.0.2.11","port":7000}
]
```

```sh
home-sound discover -inventory /absolute/inventory.json
home-sound discover -network -avahi-helper /usr/bin/avahi-browse -inventory /absolute/inventory.json
```

Inventory-only mode makes no network request. Network mode browses AirPlay/RAOP/Cast mDNS advertisements using avahi-browse and LG SSDP MediaRenderer responses. It never fetches advertised URLs, opens controls, sends Wake-on-LAN or changes devices. Discovery is bounded to five seconds and reports incomplete scans while retaining explicit inventory. A completed scan does not prove all devices are reachable. LG SSDP gives a candidate host with port 3000; the operator explicitly chooses a working WS/WSS control endpoint later.

## Bounded single-device proof

First disable only the controller-owned Homey audio source, as authorized by the controller. Do not run combined playback until single-output sound is proved. These steps were not executed against hardware during implementation.

```sh
home-sound lg-status -url ws://TV_IP:3000 -pairing-file /absolute/private/lg.json
home-sound lg-volume -url ws://TV_IP:3000 -pairing-file /absolute/private/lg.json -volume 10
home-sound airplay-status -helper /absolute/cliairplay-linux-x86_64 -helper-sha256 VERIFIED_SHA256 -host TV_IP -pairing-file /absolute/private/airplay-lg.json -target lg -lg-url ws://TV_IP:3000 -lg-pairing-file /absolute/private/lg.json
home-sound play -helper /absolute/cliairplay-linux-x86_64 -helper-sha256 VERIFIED_SHA256 -host TV_IP -pairing-file /absolute/private/airplay-lg.json -target lg -lg-url ws://TV_IP:3000 -lg-pairing-file /absolute/private/lg.json -input /absolute/probe.wav -duration 5s
```

`airplay-status` establishes and tears down a helper session, including both native LG volume checks. It is a readiness probe, not a passive status query. `lg-status` reads volume without setting it. `play` requires canonical 48 kHz stereo WAV input, limits requested duration to 30 seconds, and limits input file size to 6 MiB. It reports helper state and acknowledgement, not a claim of sound. Metadata permits Unicode, but rejects CR/LF/NUL and bounds each text field to 1024 bytes.

For the speaker proof, explicitly select `-target speaker -volume 5` and its AirPlay host/pairing file, omitting LG flags. AirPlay percentages and native LG levels do not share an acoustic scale.

## Adapter interface for the next task

`airplay.New(Config)` returns a persistent `Adapter` implementing `Output`. `Prepare(ctx, epoch, firstSample)` opens one session and keeps `ctx` as its lifetime. `Write(ctx, audio.Frame)` accepts contiguous canonical frames for that epoch, bounded to one second each; it converts to little-endian PCM16. `Start(ctx, epoch, requestedTime)` waits for the matching helper acknowledgement. `Status()` exposes health, transport readiness, validated `ClockMode`/`ClockState`, `ClockMeasurementKnown`, `ClockReady`, sanitized `HelperErrorCode`/`HelperHTTPStatus`, submitted frame count and current anchor, including `ContentCutPending`. `ClockMeasurementKnown` is false for unmeasurable NTP and for PTP cold/stalled snapshots; it is true for validated PTP probing/ready snapshots. Transport `Ready` does not prove a measured receiver clock or sound. `Metadata` submits text, and `Stop` closes the session. An adapter represents one epoch and one START; create another adapter after Stop or for another epoch rather than retaining old queued PCM.

`Config.ClockOffsetNS *int64` passes an explicit signed offset through `--clock-offset-ns`. When set, START timestamps and returned acknowledgement times are values in the helper's `CLOCK_MONOTONIC_RAW + offset` domain. The coordinator must provide that same clock domain; Task 3 does not establish a shared frozen mapping. The CLI readiness probe can pass the flag, but CLI playback rejects it because its scheduler uses wall time.
