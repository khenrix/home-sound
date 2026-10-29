package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
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
		return errors.New("usage: home-sound version|signal|inspect")
	}
	switch args[0] {
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
		output := flags.String("output", "", "output WAV file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *output == "" || *duration <= 0 || *duration > maxDuration {
			return errors.New("signal requires -duration (0 to 30s] and -output")
		}
		count := int(int64(*duration) * 48000 / int64(time.Second))
		if count == 0 {
			return errors.New("duration is shorter than one sample")
		}
		samples := make([]float32, count*2)
		for i := 0; i < count; i++ {
			v := float32(0.01 * math.Sin(2*math.Pi*1000*float64(i)/48000))
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
