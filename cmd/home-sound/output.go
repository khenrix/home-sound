package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/discovery"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/wave"
	"github.com/khenrix/home-sound/internal/webos"
)

func runOutput(args []string, out, errOut io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "discover":
		f := flag.NewFlagSet("discover", flag.ContinueOnError)
		f.SetOutput(errOut)
		inventory := f.String("inventory", "", "explicit inventory JSON")
		network := f.Bool("network", false, "browse mDNS and SSDP advertisements")
		helper := f.String("avahi-helper", "", "absolute avahi-browse path")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if f.NArg() != 0 {
			return errors.New("unexpected discovery arguments")
		}
		result, e := discovery.Discover(ctx, discovery.Config{Inventory: *inventory, Network: *network, AvahiHelper: *helper})
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(result)
	case "pair-lg", "lg-status", "lg-volume":
		return runLG(ctx, args, out, errOut)
	default:
		return runAirPlay(ctx, args, out, errOut)
	}
}
func runLG(ctx context.Context, args []string, out, errOut io.Writer) error {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(errOut)
	endpoint := f.String("url", "", "explicit ws://TV:3000 or verified wss://TV:3001 endpoint")
	file := f.String("pairing-file", "", "protected local pairing JSON")
	pin := f.String("certificate-sha256", "", "optional verified TLS certificate SHA256")
	level := f.Int("volume", 10, "native calibration volume, 0 through 15")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 || *file == "" || *endpoint == "" || *level < 0 || *level > 15 {
		return errors.New("LG requires endpoint, pairing file and calibration volume 0 through 15")
	}
	limit := 10 * time.Second
	if args[0] == "pair-lg" {
		limit = 90 * time.Second
		fmt.Fprintln(errOut, "Accept the registration prompt on the TV.")
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	c, e := webos.Connect(ctx, webos.Config{URL: *endpoint, PairingFile: *file, CertificateSHA256: *pin}, args[0] == "pair-lg")
	if e != nil {
		return e
	}
	defer c.Close()
	if args[0] == "pair-lg" {
		_, e = fmt.Fprintln(out, "LG registration saved.")
		return e
	}
	if args[0] == "lg-volume" {
		if e = c.SetVolume(ctx, *level); e != nil {
			return e
		}
	}
	v, e := c.Volume(ctx)
	if e != nil {
		return e
	}
	if args[0] == "lg-volume" && (v.Level != *level || v.Muted) {
		return errors.New("native volume readback mismatch or muted")
	}
	return json.NewEncoder(out).Encode(v)
}
func runAirPlay(ctx context.Context, args []string, out, errOut io.Writer) error {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(errOut)
	helper := f.String("helper", "", "absolute pinned cliairplay path")
	checksum := f.String("helper-sha256", "", "expected helper SHA256")
	host := f.String("host", "", "AirPlay IPv4 address")
	port := f.Int("port", 7000, "AirPlay port")
	rate := f.Int("transport-rate", 48000, "AirPlay wire sample rate: 44100 or 48000")
	timing := f.String("timing", "ntp", "AirPlay clock: ntp or required shared ptp")
	pairFile := f.String("pairing-file", "", "protected AirPlay pairing file")
	target := f.String("target", "lg", "lg or speaker")
	volume := f.Int("volume", 10, "LG must be 10; speaker initial AirPlay level 1 through 15")
	lgURL := f.String("lg-url", "", "paired native LG endpoint on the same host")
	lgPin := f.String("lg-certificate-sha256", "", "optional verified LG TLS certificate SHA256")
	lgPair := f.String("lg-pairing-file", "", "protected LG pairing file")
	input := f.String("input", "", "canonical PCM16 WAV for bounded proof")
	duration := f.Duration("duration", 5*time.Second, "maximum proof duration, up to 30s")
	title := f.String("title", "home-sound proof", "track title")
	offset := f.String("clock-offset-ns", "", "optional signed raw-clock offset supplied by a clock coordinator")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 || *duration <= 0 || *duration > 30*time.Second {
		return errors.New("proof duration must be in (0, 30s]")
	}
	cfg := airplay.Config{Helper: *helper, HelperSHA256: *checksum, Host: *host, Port: *port, PairingFile: *pairFile, Transport: airplay.Transport{Rate: *rate, Timing: *timing}}
	if _, err := cfg.Transport.Normalize(); err != nil {
		return err
	}
	if *offset != "" {
		n, e := strconv.ParseInt(*offset, 10, 64)
		if e != nil {
			return errors.New("invalid clock offset")
		}
		cfg.ClockOffsetNS = &n
	}
	if args[0] == "pair-airplay" {
		fmt.Fprintln(errOut, "Enter the device's on-screen PIN in this local terminal, then press Enter.")
		if e := airplay.Pair(ctx, cfg, os.Stdin); e != nil {
			return e
		}
		_, e := fmt.Fprintln(out, "AirPlay pairing saved.")
		return e
	}
	if *target != "lg" && *target != "speaker" {
		return errors.New("target must be lg or speaker")
	}
	if *target == "lg" && *volume != 10 {
		return errors.New("LG proof requires native volume 10")
	}
	if *target == "speaker" && (*volume < 1 || *volume > 15) {
		return errors.New("speaker proof requires explicit low volume 1 through 15")
	}
	if args[0] == "play" && cfg.ClockOffsetNS != nil {
		return errors.New("CLI playback requires wall clock; use the adapter for an externally coordinated clock")
	}
	var mapping *session.Mapping
	if cfg.Transport.Timing == "ptp" && cfg.ClockOffsetNS == nil {
		clock, err := session.NewRawClock()
		if err != nil {
			return err
		}
		frozen := session.Freeze(clock, time.Now)
		mapping = &frozen
		cfg.ClockOffsetNS = &frozen.OffsetNS
	}
	var samples []float32
	if args[0] == "play" {
		if *input == "" {
			return errors.New("play requires a canonical WAV input")
		}
		file, e := os.Open(*input)
		if e != nil {
			return e
		}
		fi, e := file.Stat()
		if e != nil || fi.Size() > 6<<20 {
			file.Close()
			return errors.New("proof WAV exceeds size limit")
		}
		format, data, e := wave.Read(file)
		file.Close()
		if e != nil {
			return e
		}
		if format != audio.Canonical {
			return errors.New("proof WAV must be canonical 48000 Hz stereo")
		}
		count := int(int64(*duration)*48000/int64(time.Second)) * 2
		if len(data) > count {
			data = data[:count]
		}
		if len(data) == 0 {
			return errors.New("empty proof input")
		}
		samples = data
	}
	ctx, cancel := context.WithTimeout(ctx, *duration+20*time.Second)
	defer cancel()
	if *target == "lg" {
		u, e := url.Parse(*lgURL)
		if e != nil || u.Hostname() != *host || *lgPair == "" {
			return errors.New("LG proof requires paired native control on the same host")
		}
		c, e := webos.Connect(ctx, webos.Config{URL: *lgURL, PairingFile: *lgPair, CertificateSHA256: *lgPin}, false)
		if e != nil {
			return e
		}
		defer c.Close()
		cfg.LG = c
		cfg.InitialVolume = 0
	} else {
		cfg.InitialVolume = *volume
	}
	adapter, e := airplay.New(cfg)
	if e != nil {
		return e
	}
	defer adapter.Stop()
	if e = adapter.Prepare(ctx, 1, 0); e != nil {
		return e
	}
	if args[0] == "airplay-status" {
		return json.NewEncoder(out).Encode(adapter.Status())
	}
	if e = adapter.Metadata(ctx, airplay.Metadata{Title: *title}); e != nil {
		return e
	}
	// Feed a small initial chunk; the persistent input ring remains paused until START.
	first := min(len(samples), 4800*2)
	if e = adapter.Write(ctx, audio.Frame{Epoch: 1, Data: samples[:first]}); e != nil {
		return e
	}

	start := time.Now().Add(3 * time.Second)
	if mapping != nil {
		start = mapping.Time(mapping.Clock.Now() + 3*time.Second)
	}
	anchor, e := adapter.Start(ctx, 1, start)
	if e != nil {
		return e
	}
	for i := first; i < len(samples); {
		end := min(i+4800*2, len(samples))
		if e = adapter.Write(ctx, audio.Frame{Epoch: 1, Sample: uint64(i / 2), Data: samples[i:end]}); e != nil {
			return e
		}
		i = end
	}
	if e = adapter.Finish(ctx); e != nil {
		return e
	}
	length, _ := audio.SampleTime(uint64(len(samples)/2), 48000)
	length += time.Duration(adapter.Status().DurationPaddingNS)
	if mapping != nil {
		if e = mapping.Clock.WaitUntil(ctx, time.Duration(anchor.At.UnixNano()-mapping.OffsetNS)+length); e != nil {
			return e
		}
	}
	wait := time.Until(anchor.At.Add(length))
	if mapping != nil {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	status := adapter.Status()
	if !status.Healthy {
		return errors.New("AirPlay proof helper stopped before completion")
	}
	return json.NewEncoder(out).Encode(status)
}
