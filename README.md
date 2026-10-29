# Home Sound

Home Sound is an early audio foundation for a planned multi-room system. The CLI generates reference WAVs, inspects files, records an explicit microphone session and analyzes a recording; it does not play audio or control devices. Real microphone capture, synchronization, and acoustic acceptance remain unverified.

Requires Go 1.27.3.

```sh
go build -o home-sound ./cmd/home-sound
./home-sound version
./home-sound signal -duration 1s -output reference.wav
./home-sound inspect reference.wav
```

`signal` generates a deterministic 1 kHz stereo sine wave at -40 dBFS peak, at 48 kHz in PCM16. The duration and output path are required; duration must be positive and no more than 30 seconds. Generation only writes a file; it does not play it. `inspect` accepts bounded PCM16 RIFF/WAVE files and reports the stored sample rate, channel count and sample-count-derived duration.

For a comparison, create a broadband reference and explicitly capture a session:

```sh
./home-sound signal -kind probe -duration 3s -output probe.wav
./home-sound record -helper /absolute/path/to/arecord -device 'hw:CARD=PCH,DEV=0' -duration 5s -output recording.wav
./home-sound analyze -reference probe.wav -recording recording.wav
```

`record` starts one arecord process only for the requested session. Duration defaults to five seconds and is limited to 60 seconds; output must be a new file and is created with mode 0600. The helper path and device must be explicitly selected (the device defaults to `hw:CARD=PCH,DEV=0`). The process requests 48 kHz stereo S16_LE PCM; actual hardware operation and capture processing have not been verified. Analysis requires both WAVs to be 48 kHz stereo and the recording to contain the entire reference. It rejects weak or ambiguous correlation, clipping, low signal-to-residual ratio, and estimated clock drift above 200 ppm. The reported peak uncertainty is the radius of correlation scores within 90% of the maximum; it is a heuristic rather than a statistically calibrated interval. The residual noise estimate also includes room response and any other departure from a scaled reference. Relative level is not calibrated SPL, and realistic acoustic performance remains unverified.

Run checks with `go test -race ./...` and `go vet ./...`. See the approved design in `docs/superpowers/specs/2026-10-29-home-sound-design.md` for the gates still required before deployment.
