package session

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/webos"
)

var ErrUnsupportedOutput = errors.New("output adapter not implemented")

type nativeOutput struct {
	airplay.Output
	native       airplay.NativeVolume
	closeNative  func() error
	changed      func(int)
	watch        sync.Once
	audioAllowed bool
}

func (o *nativeOutput) SetNative(ctx context.Context, n int) error {
	o.audioAllowed = false
	if n < 1 || n > 100 || o.native != nil && n > 15 || !o.Output.Status().Ready {
		return errors.New("native control requires prepared output, speaker level 1 through 100 or LG level 1 through 15")
	}
	if o.native == nil {
		setter, ok := o.Output.(interface {
			SetNative(context.Context, int) error
		})
		if !ok {
			return errors.New("native speaker control unavailable")
		}
		if err := setter.SetNative(ctx, n); err != nil {
			return err
		}
	} else {
		if err := o.native.SetVolume(ctx, n); err != nil {
			return err
		}
		v, err := o.native.Volume(ctx)
		if err != nil || !v.Valid || v.Muted || v.Level != n {
			return errors.New("requested native LG level was not verified")
		}
		var watchErr error
		o.watch.Do(func() {
			var updates <-chan webos.Volume
			updates, watchErr = o.native.WatchVolume(ctx)
			if watchErr != nil {
				return
			}
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case v, ok := <-updates:
						if !ok {
							return
						}
						if v.Valid && !v.Muted && o.changed != nil {
							o.changed(v.Level)
						}
					}
				}
			}()
		})
		if watchErr != nil {
			return errors.New("native LG feedback subscription failed")
		}
	}
	o.audioAllowed = true
	return nil
}
func (o *nativeOutput) Write(ctx context.Context, f audio.Frame) error {
	if !o.audioAllowed {
		return errors.New("native control must succeed before audio")
	}
	return o.Output.Write(ctx, f)
}
func (o *nativeOutput) Stop() error {
	err := o.Output.Stop()
	if o.closeNative != nil {
		if e := o.closeNative(); err == nil {
			err = e
		}
	}
	return err
}

// AirPlayFactory stores configuration only. Network work occurs exclusively when
// Engine starts an explicitly selected output; GET status never invokes it.
func AirPlayFactory(cfg config.Config) Factory {
	return func(ctx context.Context, r DeviceRequest) (Output, error) {
		if r.Device.Adapter != "airplay" {
			return nil, ErrUnsupportedOutput
		}
		c := airplay.Config{Helper: cfg.Helper, HelperSHA256: cfg.HelperSHA256, Host: r.Device.Host, Port: r.Device.Port, PairingFile: r.Device.PairingFile, InitialVolume: 1, ClockOffsetNS: &r.ClockOffsetNS}
		var client *webos.Client
		if r.Device.Kind == "tv" {
			u, err := url.Parse(r.Device.LGURL)
			if err != nil || u.Hostname() != r.Device.Host {
				return nil, errors.New("native LG endpoint must match AirPlay host")
			}
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			client, err = webos.Connect(connectCtx, webos.Config{URL: r.Device.LGURL, PairingFile: r.Device.LGPairingFile, CertificateSHA256: r.Device.CertificateSHA256}, false)
			if err != nil {
				return nil, err
			}
			c.LG = client
			c.InitialVolume = 0
		}
		out, err := airplay.New(c)
		if err != nil {
			if client != nil {
				client.Close()
			}
			return nil, err
		}
		wrapped := &nativeOutput{Output: out, changed: r.NativeChanged}
		if client != nil {
			wrapped.native = client
			wrapped.closeNative = client.Close
		}
		return wrapped, nil
	}
}
