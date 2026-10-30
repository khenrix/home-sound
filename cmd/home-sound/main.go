package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/capture"
	"github.com/khenrix/home-sound/internal/measure"
	"github.com/khenrix/home-sound/internal/wave"
)

const version = "0.1.0"
const maxDuration = 30 * time.Second

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: home-sound input-probe|serve|control|status|profile|version|signal|inspect|record|analyze|discover|pair-lg|lg-status|lg-volume|pair-airplay|airplay-status|play")
	}
	switch args[0] {
	case "input-probe":
		return runInputProbe(args[1:], out, errOut)
	case "serve", "control", "status", "profile":
		return runService(args, out, errOut)
	case "discover", "pair-lg", "lg-status", "lg-volume", "pair-airplay", "airplay-status", "play":
		return runOutput(args, out, errOut)
	case "version":
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		_, err := fmt.Fprintln(out, version)
		return err
	case "signal":
		flags := flag.NewFlagSet("signal", flag.ContinueOnError)
		flags.SetOutput(errOut)
		duration := flags.Duration("duration", 0, "signal duration, up to 30s")
		kind := flags.String("kind", "sine", "sine or broadband probe")
		output := flags.String("output", "", "output WAV file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *output == "" || *duration <= 0 || *duration > maxDuration {
			return errors.New("signal requires -duration (0 to 30s] and -output")
		}
		if *kind != "sine" && *kind != "probe" {
			return errors.New("unknown signal kind")
		}
		count := int(int64(*duration) * 48000 / int64(time.Second))
		if count == 0 {
			return errors.New("duration is shorter than one sample")
		}
		samples := make([]float32, count*2)
		state := uint32(1)
		for i := 0; i < count; i++ {
			v := float32(0.01 * math.Sin(2*math.Pi*1000*float64(i)/48000))
			if *kind == "probe" {
				state ^= state << 13
				state ^= state >> 17
				state ^= state << 5
				v = float32(int32(state)) / 2147483648 * 0.1
			}
			samples[i*2] = v
			samples[i*2+1] = v
		}
		f, err := os.Create(*output)
		if err != nil {
			return err
		}
		writeErr := wave.Write(f, audio.Canonical, samples)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	case "record":
		flags := flag.NewFlagSet("record", flag.ContinueOnError)
		flags.SetOutput(errOut)
		duration := flags.Duration("duration", 5*time.Second, "capture duration, up to 60s")
		helper := flags.String("helper", "", "explicit arecord executable path")
		device := flags.String("device", "hw:CARD=PCH,DEV=0", "ALSA capture device")
		output := flags.String("output", "", "new output WAV file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *output == "" {
			return errors.New("record requires -helper and -output")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		samples, _, err := capture.Record(ctx, capture.Config{Helper: *helper, Device: *device}, *duration)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		writeErr := wave.Write(f, audio.Canonical, samples)
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(*output)
			return writeErr
		}
		return closeErr
	case "analyze":
		flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
		flags.SetOutput(errOut)
		ref := flags.String("reference", "", "reference WAV")
		rec := flags.String("recording", "", "recording WAV")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *ref == "" || *rec == "" {
			return errors.New("analyze requires -reference and -recording")
		}
		read := func(path string) (audio.Format, []float32, error) {
			f, err := os.Open(path)
			if err != nil {
				return audio.Format{}, nil, err
			}
			defer f.Close()
			return wave.Read(f)
		}
		rf, rs, err := read(*ref)
		if err != nil {
			return err
		}
		cf, cs, err := read(*rec)
		if err != nil {
			return err
		}
		report, err := measure.Analyze(rf, rs, cf, cs)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(report)
	case "inspect":
		if len(args) != 2 {
			return errors.New("inspect requires one WAV file")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		format, samples, err := wave.Read(f)
		if err != nil {
			return err
		}
		duration, err := audio.SampleTime(uint64(len(samples)/format.Channels), format.Rate)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "PCM16 WAV: %d Hz, %d channels, duration %s\n", format.Rate, format.Channels, duration)
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
