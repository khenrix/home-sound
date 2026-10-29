# Home Sound

Home Sound is an early audio foundation for a planned multi-room system. The current CLI generates a quiet reference WAV and inspects PCM16 WAV files; it does not play audio or control devices. Device integration, real microphone capture, synchronization, and acoustic acceptance remain unverified.

Requires Go 1.27.3.

```sh
go build -o home-sound ./cmd/home-sound
./home-sound version
./home-sound signal -duration 1s -output reference.wav
./home-sound inspect reference.wav
```

`signal` generates a deterministic 1 kHz stereo sine wave at -40 dBFS peak, at 48 kHz in PCM16. The duration and output path are required; duration must be positive and no more than 30 seconds. Generation only writes a file; it does not play it. `inspect` accepts bounded PCM16 RIFF/WAVE files and reports the stored sample rate, channel count and sample-count-derived duration.

Run checks with `go test -race ./...` and `go vet ./...`. See the approved design in `docs/superpowers/specs/2026-10-29-home-sound-design.md` for the gates still required before deployment.
