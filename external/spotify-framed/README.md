# Separate GPL Spotify framed helper

This directory is GPL-3.0-or-later, including the patch and helper fixtures. It
is excluded from the Apache core's Go module by being patch data, not imported
Go packages. Upstream: https://github.com/devgianlu/go-librespot, release
v0.10.5, commit `90a4fdaf0314269e3fa25698a5f4270c026d2c03`. Canonical fetch on
2026-10-30 found master `bd3fa7407eb1ab595295446e1be50cb9663e69cb`; the build
remains pinned to the release. `SHA256SUMS` identifies this patch and GPL text.

The stock pipe backend is unchanged. Select `audio_backend: framed`,
`audio_output_pipe: /absolute/private/pcm.sock`, `external_volume: true`,
`crossfade_duration: 0`, and `normalisation_disabled: true`. The operator must
prepare protected PCM and admission Unix listeners before allowing playback.
Set `framed_instance` to the supervisor-generated process nonce and
`framed_admission_socket` to its private Unix socket; neither is an account
credential. `zeroconf_port` and `zeroconf_interfaces_to_advertise` set the
explicit operator-approved native discovery listener/interface. All identity,
REST and credential directories remain separate per configured destination.
Starting the helper advertises Connect; neither this build nor tests start a
real daemon or authenticate. Narrated/DJ sources fail closed; ordinary tracks
only. Framing establishes relative decoded sample continuity, not exact
original-track seek accuracy.

Build into a new directory with Go 1.27.3 and native ALSA, Vorbis, Ogg, FLAC,
mpg123 development dependencies:

```
GO_BINARY=/absolute/go/bin/go external/spotify-framed/build.sh \
  /absolute/new/helper-source /absolute/output/go-librespot-framed
```

`PKG_CONFIG`, `PKG_CONFIG_SYSROOT_DIR`, `PKG_CONFIG_PATH` and
`LD_LIBRARY_PATH` may point at a private extracted distro sysroot. The script
checks patch hashes, acquires canonical corresponding source, applies the
framed patch followed by the native decoder patch and copies the pinned synthetic
tone fixture, runs uncached upstream unit tests with race detection, vets and builds.
It never installs, starts, advertises or authenticates. `build-dependencies.json`
records the exact Ubuntu archive artifacts used for local verification; their
copyright notices are retained under the extracted sysroot's `usr/share/doc`.
Those archives and native libraries are not shipped with the core.

When distributing a helper binary, supply the complete patched corresponding
source, this patch, build configuration, upstream and dependency notices, and
GPL license. A process boundary is an engineering separation, not a legal
claim about arbitrary combined distributions. The generated binary hash and
actual build information must accompany deployment. No binary is committed. `build-verification.json` records the current patches,
source pin, toolchain, native dependency manifest and independently reproduced
binary hashes; it is software build evidence, not live acceptance.

## Native Vorbis decoder cleanup

Apply `native-decoder.patch` after the unchanged protocol-v2 `framed.patch`.
The upstream release and its Go/native dependency pins are unchanged. The only
additional production change tears down block state, then DSP state, then codec
info, retaining the existing nil-reference guards. libvorbis DSP teardown reads
`vorbis_info` and its codec setup; clearing/freeing that info first leaves a
dangling C pointer. The matching canonical [libvorbis v1.3.7 source](https://github.com/xiph/vorbis/blob/v1.3.7/lib/block.c)
and [reference decoder](https://github.com/xiph/vorbis/blob/v1.3.7/examples/decoder_example.c)
show the dependency and teardown order.

The patch includes actual native decoder Close-after-Read and Close-before-Read
checks, repeated Close and rejected Read-after-Close, invalid/partial-header
cleanup, and real player peer revocation/session retirement followed by a fresh
player owner. The separate core fixture also decodes non-silent native PCM on
both sides of a revoked owner, rejects a stale native command, and requires a
clean helper exit after both decoders retire. These fixtures never start a
production daemon, advertise, authenticate, or access an account. Core Go APIs
and wire protocol v2 are unchanged. This establishes the local cleanup fix;
live Spotify handoff acceptance remains separate.

`native-decoder-tone.ogg` is a newly synthesized 440 Hz tone, five seconds,
44100 Hz stereo Vorbis, distributed under this directory's GPL license. It
contains no account data or recording. Generated with Ubuntu's FFmpeg
8.0.1-3ubuntu2; this command was rerun and produced the identical SHA-256 in
`SHA256SUMS`:

```sh
ffmpeg -hide_banner -loglevel error -f lavfi \
  -i sine=frequency=440:sample_rate=44100:duration=5 -ac 2 \
  -c:a libvorbis -q:a 4 -map_metadata -1 \
  -fflags +bitexact -flags:a +bitexact native-decoder-tone.ogg
```

The build copies it to `vorbis/testdata/close-tone.ogg`. Both clean builds
produced binary SHA-256 `59f86151e38c11d5d10a70127d1140d30e14f7d8c78ceae2f92a7741344bf8c1`.
The matching corresponding-source archive hash and deterministic archive recipe
are recorded in `build-verification.json`. The previous helper artifact is
retained separately; no deployment is implied by changing this build pin.

## Protocol version 2

A native play/transfer command requests admission before asynchronous loading.
The Dealer stamps the command with Linux `CLOCK_MONOTONIC_RAW` before queuing;
the helper supplies that receive marker, a process-wide increasing sequence and
its configured instance. The local core rejects commands received before a later
handoff, then grants an opaque token. IPC is capped at 500 ms and bounded JSON.
Already-current retaining transfers keep their grant; a genuine fresh recast
replaces a revoked grant. Seeks and prefetched streams retain immutable grants.
Grant revocation cancels the old PCM connection. Reconnect requires a fresh
native grant and new output nonce. Tokens are never persisted or logged.
WebSocket/REST active notifications cannot mint grants. The core and framed
helper must share the same Linux host clock; unsupported hosts fail closed.

The helper connects to the configured private Unix stream. Each output
instance generates a fresh 128-bit lowercase hexadecimal `Nonce`. A stopped
or replaced output therefore cannot impersonate its successor even in the
same helper process. The core identifies the configured group independently.

The consumer sends one byte per response: `1` requests up to 882 stereo frames
(20 ms); `2` requests status without decoding. Only one request may be
outstanding. A paused `1` request returns a status record with no PCM. Poll
paused state at a bounded rate; do not busy-loop. Status observes the tagged
source under its lock. Stock WebSocket metadata is never an audio boundary.

Responses are a four-byte unsigned big-endian length (1–32768), followed by
one UTF-8 JSON object. Field names are case-sensitive:

- `Version`: 2; `Nonce`: 32 hexadecimal characters.
- `Generation`: monotonic source generation, changed by a genuinely new
  source, successful seek, and automatic prefetch promotion.
- `Offset`: relative decoded stereo frame offset in that generation.
- `Identity`: bounded `uri`, `title`, optional `artist`, `album`, `artwork_id`,
  `artwork_url`, plus private `instance` and `admission` authority. Artwork uses
  `spotify:image:<40 lowercase hex>` and its matching `https://i.scdn.co/image/`
  URL. Missing artwork clears the previous identity at the content boundary.
- `Reason`: `source`, `seek`, or `prefetch`; repeated for the generation.
- `Rate`: 44100; `Channels`: 2.
- `PCM`: base64 of interleaved little-endian IEEE float32 bytes; finite only,
  at most 7056 bytes. The offset advances by decoded frames, never milliseconds.
- `Transport`: `playing` or `paused` observed for this serialized response.
- `Terminal`: empty, `track_end`, or `error`. `track_end` pauses production;
  it is not whole-queue EOF. A socket close terminates that output instance.

Metadata is attached before source installation, including prefetch. An old
track's nonempty final read retains its generation even when that same read
promotes the next track. Re-acknowledging that prefetched source does not
increment generation. Failed seek does not reset the frame offset.

Borrowed prefetch loads reuse the stream's canonical wrapper. Nonzero-position
loads enter the player's framed seek command; the switching-source lock covers
both decoder seek and installation, even when Read already promoted it. One
successful command creates one generation and clears a pending old EOF notice.
The output stays paused across this boundary until the requested transport is
restored. Cancellation before the locked operation does not seek or close the
borrowed decoder; cancellation after that operation starts is not a transactional
rollback of the decoder. Native seek failure position recovery remains unverified.

There is one serialized writer and no PCM queue. The source mutex is released
before writing. Writes have a 250 ms deadline; controls have a 500 ms bound.
Pause and Drop acknowledge only after the prior response has completed or the
connection has failed. Drop leaves production paused until explicit Resume.
An acknowledged barrier cannot have a later old response in that writer;
bytes already received by the core must still be discarded by owner/generation
when replacing a session. Closing the socket interrupts blocked transport.
Unexpected read-side EOF also
notifies the player through its output error channel, so the next admitted
stream creates a fresh socket and nonce. Intentional local Close does not
report that expected read-side shutdown as a failure.
A decoder blocked inside its own Read remains an upstream cancellation limit:
process-group kill/reap is required as the outer shutdown deadline fallback.

The conforming one-credit rule bounds helper/socket PCM to one 20 ms record;
converter/filter and core pending data have their own explicit accounting.
A misbehaving peer issuing many credits can fill the socket but must still not
block controls indefinitely. The actual helper subprocess fixture exercises
that case. This does not establish live Spotify or speaker acceptance.
