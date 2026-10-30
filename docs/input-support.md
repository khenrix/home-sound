# Native inputs and isolated PCM observation

Task 5 supplies two Spotify Connect identities and experimental YouTube
DIAL/Lounge identities named exactly **TV + All Speakers** and **All Speakers**.
They compete for one source owner. Native Android discovery, real account
playback, acoustic output and long-running household reliability remain separate
acceptance gates. This is not a universal Google Cast receiver.

Inputs are disabled by default. `serve` starts no input process, DIAL listener or
advertisement unless `-enable-inputs -inputs-config /absolute/inputs.json` is
supplied. Each provider also needs `enabled: true` in that protected configuration.
Starting Spotify advertises Connect. Enabling YouTube binds DIAL HTTP and SSDP;
Lounge registration and media subprocesses begin only on a DIAL launch.

## Protected configuration

Use a regular mode-0600 JSON file and an absolute mode-0700 state directory with
real directory ancestors. Runtime state is separate for the two identities and
providers. Stable device IDs and UUIDs live in `identities.json`; the helper owns
its private Spotify credentials. Core never reads those credentials. YouTube
persists only its screen ID, not Lounge tokens or transient session cursors.
Existing runtime/socket paths are never deleted to take over another owner.

This template deliberately enables nothing and contains no credentials:

```json
{
  "version": 1,
  "state_dir": "/home/operator/git/home-sound/.runtime/inputs",
  "bind_ip": "192.168.1.20",
  "interface": "eth0",
  "volume": {"min_db": -50, "max_db": -30, "initial_percent": 20},
  "spotify": {
    "enabled": false,
    "helper": {"Path": "/absolute/go-librespot-framed", "SHA256": "<whole executable SHA-256>"},
    "ports": [38401, 38402],
    "zeroconf_ports": [38421, 38422]
  },
  "youtube": {
    "enabled": false,
    "native_accepted": false,
    "ports": [38411, 38412],
    "framing": "bytes",
    "resolver": {"Path": "/absolute/yt-dlp", "SHA256": "<whole standalone bundle SHA-256>"},
    "runtime": {"Path": "/absolute/node", "SHA256": "<whole executable SHA-256>"},
    "decoder": {"Path": "/absolute/ffmpeg", "SHA256": "<whole executable SHA-256>"},
    "decoder_version": "n9.0.2"
  }
}
```

The relative-dB endpoints are operator policy, not acoustic calibration. Every
speaker admission and target change still passes the existing verified profile
and quiet-reference ceiling checks. Percentages map to that common dB range;
they are never sent directly as TV native percentages. A new seek retains the
accepted common target and mute. Native volume zero silences the bus. Spotify starts at the configured initial percentage,
ignores saved volume, uses external volume and disables normalization, crossfade,
autoplay and optional audio/metadata caches. REST remains loopback-only.

YouTube framing must be explicitly `bytes` or `utf16`. Neither mode guesses after
malformed input. Current Android/Lounge Unicode length units have not been
measured on household hardware. Select and verify the mode during the native
acceptance probe; ASCII fixtures alone do not decide it.

## Listener and firewall inventory

The template above allocates these ports, in identity order **TV + All Speakers**,
then **All Speakers**. Both providers remain disabled until explicitly enabled.

| Purpose | Bind/destination | Template ports |
| --- | --- | --- |
| Spotify REST/WebSocket control | Loopback only, `127.0.0.1` | TCP 38401, 38402 |
| Spotify native ZeroConf HTTP | Upstream binds `0.0.0.0`; restrict ingress to approved LAN senders/interface | TCP 38421, 38422 |
| Spotify mDNS `_spotify-connect._tcp` | Selected `interface`, multicast `224.0.0.251` / `ff02::fb` | UDP 5353 |
| YouTube DIAL HTTP | Explicit `bind_ip` (`192.168.1.20` in template) | TCP 38411, 38412 |
| YouTube SSDP | Selected `interface`, multicast `239.255.255.250` | UDP 1900 |
| PCM, native admission, runtime and operator control | Protected Unix sockets under private directories | No TCP/UDP port |

Set `bind_ip` and `interface` to the actual reviewed host/LAN before running.
Spotify advertisement interface selection does not change its upstream HTTP
wildcard bind; the host firewall must restrict native HTTP exposure. All six
configured TCP ports must be distinct and unprivileged; no ephemeral ZeroConf
port is selected. No firewall rules are installed by this runtime or template.
Outbound provider HTTP/TLS and Spotify access-point connections remain required;
this inventory covers inbound listener allowances, not a provider egress list.
The source service/admission clock currently requires Linux.

## Isolated probe and service

After provisioning and verifying helper artifacts, an operator can enable the
wanted providers and run:

```sh
home-sound input-probe -enable-inputs -config /absolute/inputs.json -duration 5m
```

The probe has no speaker factory or profile configuration. It consumes finite
canonical PCM at sample-derived real-time intervals, retaining at most one frame
while paused. The bounded report includes group/epoch, format, frame count,
peak/RMS, metadata and final transport for up to 64 observations. `audible` and
`native_accepted` are always false: PCM observation cannot certify either claim.
Duration must be positive and at most 30 minutes. SIGINT/SIGTERM cancel and reap
owned helpers. The worker's tests use controlled loopback sockets and subprocess
fixtures, not this live command with enabled providers.

For speaker routing, use the existing protected service configuration as well:

```sh
home-sound serve -socket /private/operator.sock -config /private/speakers.json \
  -enable-inputs -inputs-config /private/inputs.json
```

YouTube service startup rejects `native_accepted: false`. Set that marker only
after the controller records native Android-button discovery of **both names**,
launch/control, sustained decoded PCM, restart and reconnect in the isolated
probe. It does not waive any speaker/profile gate. Spotify real playback and
controls also require controller acceptance. No live acceptance is supplied by
these software fixtures.

## Continuity and ownership

The GPL helper's framed protocol is documented in
[external/spotify-framed/README.md](../external/spotify-framed/README.md).
The core validates one bounded record per credit: nonce, generation, format,
relative offset, immutable metadata and finite stereo f32le. It converts actual
44.1-kHz PCM through the existing stateful rational FIR to 48-kHz canonical PCM.
Status credits keep paused/backpressured controls observable without decoder
reads. A socket contains at most one outstanding record; core keeps one input
packet plus converter/normalizer state. Child stdout pipes are explicitly 4096
bytes on Linux. A one-second source reserve reduces engine production lead from
three seconds to two, accounting for that input PCM buffering and codec/filter
state; queued encoded media is not decoded PCM or a presentation clock.

A helper PCM generation and bus epoch are different identities. Explicit
seek/source replacement, another owner and reconnect flush the old bus via the
existing engine and discard its buffers. Natural same-instance `prefetch`
promotion keeps the converter phase, absolute canonical sample count and output
connections. Metadata is scheduled at `ceil(input_frames * 160 / 147)` on that
same timeline; old-tail samples remain before the boundary. A final helper EOF
pause is production completion, not a request to truncate already queued audio.

Every control/result carries provider, group, instance, helper generation and
coordinator epoch. Ownership is reserved before asynchronous YouTube resolution;
a late result is closed rather than stealing a newer selection. Old volume,
metadata, transport and termination callbacks cannot mutate a successor.
Native Spotify commands reserve ownership through private bounded admission IPC
before loading. Process identity, command sequence and a shared-host monotonic
receive marker fence queued commands across handoffs. Revoked grants close their
PCM connection; a genuine new native selection obtains a fresh grant/output nonce.
A current retaining transfer and ordinary pause/resume preserve media.
WebSocket messages trigger bounded REST reconciliation; their metadata and
arrival order never label PCM. Expected normalized volume echoes are bounded.
An operator WAV replacement/stop revokes native ownership atomically.

Artwork identity and canonical HTTPS URL are bounded and validated for both
providers, carried with generation-tagged metadata through session status and
the output metadata interface. Spotify image IDs and YouTube video IDs must
match their allowlisted public image URL. A new track without artwork clears the
prior identity; stale metadata cannot restore it. Image-byte fetching, native
AirPlay artwork transmission and receiver screen rendering belong to Task 6 and
physical acceptance. URL propagation does not claim rendered artwork.

## YouTube scope

Supported software boundaries are DIAL discovery/launch/stop; Lounge screen/token
registration, pairing and cursor-preserving poll/rebind; explicit playlists,
play/pause/seek/next/previous, volume/mute and state reports; and bounded media
resolution/decoding. Natural EOF advances only the explicit supplied playlist, after the Engine
finishes presenting queued PCM. Final items also drain before stopping. Reported
position follows presentation, including pause/resume, rather than decoder reads.
EOF of a Lounge HTTP poll reconnects; it is not media EOF. Seek starts a fresh
source while preserving pause. Decode startup failure or expired media permits
at most one fresh resolution. Processes use explicit argv, bounded stdout/stderr,
process-group cancellation and a single reaper. A helper that closes stdout
without exiting is terminated after two seconds. Diagnostics expose generic
errors, never signed URLs, tokens or whole upstream payloads.

Only public/unlisted, non-live, unrestricted, non-DRM ordinary video audio is
admitted, up to six hours, with Opus/AAC audio-only resolution. There is no cookie
file, browser-cookie extraction, account/password flow, token service, geo-header
spoofing, sign-in/age/private/rental/DRM bypass, plugin or remote JS-component
loading. YouTube Music, automatic recommendations, ads/narrated/DJ semantics and
universal Cast compatibility are unsupported. Exact original-track seek position
is unverified; relative decoded sample zero is not proof of content-time accuracy.
Native media errors after playback begins terminate that source; they are not
silently converted into another item or successful PCM.

## Pins, licenses and repeatable fixtures

- go-librespot v0.10.5: `90a4fdaf0314269e3fa25698a5f4270c026d2c03`, separate
  GPL-3.0-or-later patch/build, native dependencies and reproducible binary hashes
  under `external/spotify-framed`. Protocol v2 adds native admission and artwork
  to the Stage A source boundary; use its newly pinned binary/patch together. The Apache core imports no helper Go packages.
- yt-dlp 2026.10.26: `f264a6cbd1f630894da0f368df00f8aae50140e0`; supported EJS
  dependency 0.8.0. Provision a verified **standalone release bundle**, retain its
  third-party license notices, and hash the complete bundle. A Python entrypoint
  hash does not pin an installed package. The adapter verifies the exact version,
  disables user config/cache/plugins/remote components, and explicitly selects a
  separately hashed Node executable (major 20 or newer). No deployment resolver
  bundle was acquired or tested on real YouTube by this worker.
- FFmpeg n9.0.2: `946fcce07b6dcd0331c8cc609192aeff5e1924f8`. Provision a separately
  hashed build with its actual build configuration, licenses and dependencies.
  Runtime configuration accepts the exact release spellings `9.0.2` and `n9.0.2`;
  the configured spelling must match the binary version output. The local decoding fixture uses the
  installed GPL-enabled `8.0.1-3ubuntu2`; it does not establish a 9.0.2 deployment
  build. Its SHA-256 is
  `bdf6aabffdba7411edff8d36c389d695257fcdf823d196020176e117612862f6`.
- plaincast `778493916bbd71a85c8457c35dcc33641805a1e6`: permissive protocol
  adaptation; BSD-2-Clause notice in `internal/youtube/NOTICE`. Unknown-license
  yt-cast code was behavior evidence only and was not copied.

A separate test-only GPL overlay compiles the actual patched switching source
and framed output into a subprocess fixture:

```sh
GO_BINARY=/absolute/go/bin/go external/spotify-framed/build-core-fixture.sh \
  /absolute/patched-corresponding-source /absolute/core-framed-fixture
HOME_SOUND_FRAMED_FIXTURE=/absolute/core-framed-fixture \
  /absolute/go/bin/go test -race -count=1 ./...
```

Use the native-library environment described in the helper README when needed.
The actual-source test verifies seek/pause/generation/metadata and natural
prefetch across the Apache parser, one continuous converter, and a real Engine
with injected outputs. It performs no Spotify authentication or advertisement.
Without the explicit fixture path those integration tests report a skip;
final Task 5 verification supplies it. The FFmpeg fixture decodes a local HTTP
WAV into exact canonical PCM. Neither fixture establishes live service support.
