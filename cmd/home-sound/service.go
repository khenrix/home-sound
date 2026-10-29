package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/control"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/volume"
)

func runService(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	socket := flags.String("socket", "", "absolute operator Unix socket path")
	path := flags.String("config", "", "protected version 1 configuration file (serve)")
	action := flags.String("action", "", "select, stop, pause, resume, target, mute or exclude (control)")
	group := flags.String("group", "", "configured destination (select)")
	input := flags.String("input", "", "canonical reference WAV (select)")
	first := flags.Uint64("first-sample", 0, "absolute source sample for new selection/seek")
	device := flags.String("device", "", "member ID (profile or exclude)")
	epoch := flags.Uint64("epoch", 0, "current generation (exclude)")
	file := flags.String("file", "", "profile JSON file to save; omit to inspect")
	preview := flags.Bool("preview", false, "show config readiness rather than session status")
	var target *float64
	var native *int
	var muted *bool
	flags.Func("target-db", "common relative dB target", func(s string) error {
		v, e := strconv.ParseFloat(s, 64)
		if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("finite target-db required")
		}
		target = &v
		return nil
	})
	flags.Func("tv-calibration", "TV-only native level 1 through 15; supplies no acoustic calibration", func(s string) error {
		v, e := strconv.Atoi(s)
		if e != nil || v < 1 || v > 15 {
			return errors.New("native calibration level 1 through 15 required")
		}
		native = &v
		return nil
	})
	flags.Func("muted", "true or false (mute)", func(s string) error { v, e := strconv.ParseBool(s); muted = &v; return e })
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *socket == "" {
		return errors.New("operator command requires -socket and no positional arguments")
	}
	if args[0] == "serve" {
		if *path == "" {
			return errors.New("serve requires -config")
		}
		cfg, err := config.Load(*path)
		if err != nil {
			return err
		}
		clock, err := session.NewRawClock()
		if err != nil {
			return err
		}
		mapping := session.Freeze(clock, time.Now)
		service, err := control.New(*path, cfg, mapping, session.AirPlayFactory(cfg))
		if err != nil {
			return err
		}
		defer service.Close()
		listener, err := control.Listen(*socket)
		if err != nil {
			return err
		}
		defer listener.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return control.Serve(ctx, listener, service)
	}
	method, endpoint := http.MethodGet, "/v1/status"
	var body any
	switch args[0] {
	case "status":
		if *preview {
			endpoint = "/v1/config"
		}
	case "control":
		method = http.MethodPost
		endpoint = "/v1/control"
		body = control.Command{Action: *action, Group: *group, Input: *input, FirstSample: *first, TargetDB: target, TVCalibration: native, Muted: muted, Epoch: *epoch, Device: *device}
	case "profile":
		endpoint = "/v1/profiles"
		if *file != "" {
			if *device == "" {
				return errors.New("profile update requires -device")
			}
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			decoder := json.NewDecoder(io.LimitReader(f, 16385))
			decoder.DisallowUnknownFields()
			var profile volume.Profile
			err = decoder.Decode(&profile)
			if err == nil && decoder.Decode(new(any)) != io.EOF {
				err = errors.New("trailing profile data")
			}
			f.Close()
			if err != nil {
				return errors.New("invalid profile JSON")
			}
			if err = profile.Validate(); err != nil {
				return err
			}
			method = http.MethodPut
			body = map[string]any{"device": *device, "profile": profile}
		}
	}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequest(method, "http://unix"+endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := control.Client(*socket)
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("operator socket request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("operator rejected request: %s", bytes.TrimSpace(detail))
	}
	_, err = io.Copy(out, io.LimitReader(response.Body, 1<<20))
	return err
}
