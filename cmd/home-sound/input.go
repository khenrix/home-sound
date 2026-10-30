package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/khenrix/home-sound/internal/inputs"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func runInputProbe(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("input-probe", flag.ContinueOnError)
	flags.SetOutput(errOut)
	enable := flags.Bool("enable-inputs", false, "explicitly permit configured native adverts and helpers")
	path := flags.String("config", "", "private input configuration")
	duration := flags.Duration("duration", 0, "bounded PCM observation duration, at most 30m")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if !*enable || *path == "" || *duration <= 0 || *duration > 30*time.Minute || flags.NArg() != 0 {
		return errors.New("input-probe requires -enable-inputs, -config and -duration (0,30m]")
	}
	cfg, e := inputs.Load(*path)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	observer := source.NewObserver()
	defer observer.Stop()
	co := source.NewCoordinator(observer, cfg.Volume.Target)
	runtime, e := inputs.Start(ctx, cfg, co, true)
	if e != nil {
		return e
	}
	defer runtime.Close()
	select {
	case <-ctx.Done():
	case e = <-runtime.Errors():
	}
	runtime.Close()
	observer.Stop()
	report := struct {
		Audible        bool                 `json:"audible"`
		NativeAccepted bool                 `json:"native_accepted"`
		Observations   []source.Observation `json:"observations"`
	}{Observations: observer.History()}
	if je := json.NewEncoder(out).Encode(report); je != nil {
		return je
	}
	return e
}
