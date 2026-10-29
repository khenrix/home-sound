# Home Sound approved design

Approved in conversation on 2026-10-29. Implementation is incremental: measured evidence precedes claims of device support.

## Purpose and approach

Replace the experimental Homey audio stack with a lightweight Go application in github.com/khenrix/home-sound. The chosen approach is Go-owned coordination with narrow protocol/native adapters. A strict Go-only rewrite was rejected because it would add hardware-driver and protocol work without proving acoustic synchronization.

The deployment target is the Intel MacBookAir6,2 running Ubuntu Linux, reached through SSH alias macbook-air. It has four logical CPUs and 4 GB RAM. Wait for the OS upgrade owner to verify completion before changing it. Deploy directly under systemd, using an unprivileged service identity, versioned releases and rollback. No Docker requirement.

## Experience

Expose exactly two virtual group destinations: TV + All Speakers (LG C1 built-in speakers, Sonos One Kök, Sonos Roam, Hall Nest Hub 2), and All Speakers (both Sonos and Hall). Keep native individual destinations available. Overlapping groups have a single session owner. A new selection replaces the old session and removes excluded outputs, including the TV.

Spotify uses Connect. YouTube must work through the existing Android Cast button. An unofficial YouTube-specific DIAL/Lounge adapter is acceptable, but both discovery and sustained decoded audio must be proven on the actual phone. Audio, artwork and controls come first; video and lip sync are deferred. No claim of universal Cast input for other applications.

Fresh selection wakes/foregrounds included devices and clears previous manual exclusions. A deliberate TV Home/Back or Hub foreign-app switch excludes that output until a fresh selection; recovery must not fight the user. Transient loss recovers only the affected output at the current timeline. Healthy outputs continue. Receiver restart invalidates the previous clock epoch.

## Audio and interfaces

Go owns session generations, audio routing, timing, volume, recovery and diagnostics. Canonical audio is 48 kHz stereo float32 with integer sample positions. Source, Output and Capture interfaces isolate protocol boundaries. Outputs advertise readiness, acknowledged start times, clock quality, metadata/control support and health. Use bounded three-second queues, sample-derived presentation times and clock feedback, not a Go ticker or fixed offsets alone.

Use a pinned music-assistant/airplay-cli helper for direct LG/Sonos output. Shared PTP is used only where verified; test an explicit NTP route if the LG does not answer PTP. The external Chromecast is not an audio member; it may remain a CEC wake fallback. The LG native volume is the single TV volume control.

Hall uses an owned thin JavaScript Cast receiver with the full maintained Sendspin client and a matching Go compatibility slice. The user accepts the one-time Google developer registration. Pin JS commit 7282880e1bba873e927080c98482246060515ce6 and matching specification f417b498e294d34c2c788918724443b6898f972f, subject to source validation before implementation. Do not mix npm releases with specification HEAD. Initially serve an unpublished registered app over same-origin LAN HTTP/WebSocket, which Google permits for development; prove that Hall accepts this deployment. Persist pairing identity deliberately and verify reload behavior. This is a tested Hall slice, not full Sendspin conformance.

Keep receiver UI to current track, artwork, progress and state. Debug text belongs in diagnostics. LG native AirPlay text/artwork must pass an actual screen check; protocol acknowledgements do not establish rendering. Do not introduce an LG developer app or new hardware without a revised decision if the native path fails.

## Measurement and loudness

First prove continuous raw PCM from the Mac's actual microphone, including routing, capture format, processing and cancellation. Capture only during explicit measurement sessions. Use the same microphone/settings/orientation for comparisons. Absolute SPL is unverified without a calibrated microphone; report relative dB and confidence honestly.

One-metre measurements establish per-device volume curves. A fixed central listening position establishes TV/Kök/Hall timing and separate room trims. Roam stays in the office and is excluded from central calibration; its prior one-metre curve is provisional. Do not equalize office Roam at the central position.

Start with TV native volume 10. Hardware tests must never exceed TV native volume 15 or its measured acoustic reference for other devices. Other devices' native percentages may be higher if their measured sound remains below that reference. No loud upper-limit tests. Reject clipped, noisy or uncertain readings, and store confidence, background level, geometry and repeatability.

Map group changes to the same relative-dB target through monotonic measured curves, native steps and attenuation. Preserve separate room trims. At an attainable maximum, a device saturates while others continue; it resumes following when the target returns to range. Do not pretend extrapolated/unmeasured regions are verified. Start operation in the measured quiet range. TV remote changes update group target without feedback loops.

## Required microphone investigation

The Mac microphone is the first measurement implementation. Real PCM access from Sonos One/Roam and Nest Hub microphones remains a separate required investigation. Distinguish the LG enclosure from the Magic Remote microphone. Public API absence does not prove impossibility. Use public source and legitimately obtained official installed packages with REA where needed. No leaked proprietary firmware, credential extraction, rooting or flashing by default. Require reproducible samples, source attribution, format, timing, mute behavior and reconnect evidence. A mic status flag or speech-to-text does not pass. If blocked, retain that explicit blocker; do not silently substitute another sensor or claim completion.

## Acceptance and operations

Target acoustic pairwise arrival difference <=10 ms for TV/Kök/Hall at the central position, with uncertainty <=2 ms; repeat at startup, 10 and 30 minutes. Volume agreement and changes target <=1 dB while unsaturated. Validate 30 minutes of real playback without recurring audible stutters or measured gaps >100 ms. Packet counts and reported playback alone do not pass.

Test source/group switches, seek, stale events, service restart, ten-second device/network interruption, manual exits and recasting. Verify three normal TV standby wakes <=30 s from selection to audible playback, plus plugged-in Quick Start+ Off 30-minute deep standby <=90 s without intervening wake probes. Restore Quick Start+ On afterward. Leave Screen Saver Promotion Off.

Targets: Go <=64 MiB RSS, complete steady runtime <=256 MiB, streaming CPU <=one logical core on this i5. Temporary build/REA processes are not runtime. Bound storage/queues; use atomic versioned JSON without a database service. Keep credentials/pairing material protected outside Git; never expose secrets in logs or reports. Original code defaults to Apache-2.0; preserve dependency licenses and do not copy the private stream-audio-go repository.

## Cutover

Inventory only owned Homey/PC components. Preserve configuration, calibration and a supported protected account-data backup with a verified restore route before uninstalling. Release archives alone are insufficient for private data. Disable our Homey audio app before Go hardware tests and verify owned listeners/helpers exit. Leave unrelated apps, automations and native Sonos/Cast devices untouched. Uninstall only after replacement acceptance and restore verification; otherwise leave the old app disabled with data intact. Stop only exact owned temporary processes/container/ADB server, and restore Samsung Auto Blocker with human assistance at final cleanup.

Failed input, receiver, microphone or acoustic gates block dependent work and completion claims. Record the evidence and exact limitation before choosing a new approach.
