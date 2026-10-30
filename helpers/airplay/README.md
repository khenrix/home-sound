# Experimental native NTP helper candidates

These patches target canonical `music-assistant/airplay-cli` release v0.5.9,
commit `0a58c239d5fb3cf8927b9707a71c6fe897266b6a`. They are separate experimental
executables, selected only by an explicit path and SHA256. They do not replace
or install the official helper. The Go core continues to communicate through a
process boundary; it does not link the helper or its dependencies.

`anchor.patch` maps the NTP sync packet's first RTP sample onto the commanded
START content line. Its timestamp comes from `start_ntp` plus the full scheduling
head displacement minus the advertised lead. The media send head may move with
queue depth, but each announced sample remains on the same line. The wire RTP
subtraction wraps modulo 2^32. No empirical room offset is introduced.

`epoch.patch` adds the 1900-to-1970 epoch delta only when serializing media sync
seconds. This matches the native helper's NTP timing responder. Internal START,
pacing, and Unix-millisecond acknowledgments keep their existing convention.

The `combined` candidate applies both independent patches. These source and
packet corrections do **not** establish acoustic synchronization, receiver epoch
interpretation, or hardware compatibility. Controller-led A/B testing remains
required, including corrected START/content-cut behavior and long-run drift.

## Reproduce

Use a canonical checkout with v0.5.9 and all recursively pinned submodule Git
objects available. The script exports immutable Git objects, not working-tree
files, and requires a new destination. It builds Linux x86_64 using GCC/G++, GNU
make, Python 3.12+, and the release's bundled library archives. No privileged
ports or devices are used by the tests. Commands run with umask 022.

```
python3 helpers/airplay/tests/run.py /path/to/canonical/airplay-cli anchor
python3 helpers/airplay/tests/run.py /path/to/canonical/airplay-cli epoch
python3 helpers/airplay/build.py /path/to/canonical/airplay-cli /new/candidate-directory
```

The first two commands must fail on the untouched pin: the baseline clamps the
RTP wrap to zero and pairs queue-head-derived samples with current time; its
media seconds differ from the responder by 2208988800. The builder runs the
corresponding passing tests after each patch and tests both behaviors combined.
It retains complete exported source, patches in this repository, build logs,
license notices, compiler version, recursive source pins, patch hashes, build
commands, and SHA256 for each separately named artifact in `manifest.json`.
`STATIC=1` bundles the upstream codec/crypto archives; it does not produce a
libc-independent executable. Matching source/toolchain inputs are required;
reproducibility across different toolchains is not claimed.

The tests compile the actual static media emitter and actual timing responder
from the selected source. UDP sockets bind only to ephemeral loopback ports.
Only the host clock is controlled; neither wire algorithm is reproduced in Go
or replaced with a fake encoder/responder. Cases include varying pacing heads,
varying current wall times, negative lead displacement, timestamp wrap, packet
headers, and media/responder epoch agreement.

## Licenses and source retention

The modified `ap2_client.c` is Apache-2.0, with upstream copyright intact and
changes marked. The combined helper executable is GPL-3.0, as stated by upstream
`LICENSE` and `THIRD_PARTY_NOTICES.md`; it is not licensed as part of the Apache
Go core. The builder retains those files and every recursively exported
submodule's license/source files next to the artifacts. Conveying a modified
binary requires its corresponding source, these patches and build instructions,
and the upstream notices. Keep the build's source directory with any distribution.

Upstream's notices explicitly identify an unresolved license-grant ambiguity
for libraop `pairing.cpp` and `bplist.cpp`; these patches do not resolve or hide
that existing upstream issue. No upstream publication is performed here.
