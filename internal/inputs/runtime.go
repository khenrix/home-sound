package inputs

import (
	"context"
	"errors"
	"fmt"
	"github.com/khenrix/home-sound/internal/control"
	"github.com/khenrix/home-sound/internal/source"
	"github.com/khenrix/home-sound/internal/spotify"
	"github.com/khenrix/home-sound/internal/youtube"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Runtime struct {
	cancel  context.CancelFunc
	workers sync.WaitGroup
	closers []func() error
	once    sync.Once
	errors  chan error
}

func (r *Runtime) Errors() <-chan error { return r.errors }
func (r *Runtime) fail(class string, slot, exit int) {
	select {
	case r.errors <- fmt.Errorf("input runtime stopped: %s slot=%d exit=%d", class, slot, exit):
	default:
	}
	r.cancel()
}
func (r *Runtime) Close() error {
	r.once.Do(func() {
		r.cancel()
		for n := len(r.closers) - 1; n >= 0; n-- {
			_ = r.closers[n]()
		}
		r.workers.Wait()
	})
	return nil
}
func Start(parent context.Context, c Config, co *source.Coordinator, probe bool) (r *Runtime, err error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(parent)
	r = &Runtime{cancel: cancel, errors: make(chan error, 1)}
	owned := r
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	if !c.Spotify.Enabled && !c.YouTube.Enabled {
		return r, nil
	}
	if co == nil {
		return nil, errors.New("input coordinator required")
	}
	if c.YouTube.Enabled && !probe && !c.YouTube.NativeAccepted {
		return nil, errors.New("YouTube speaker routing requires recorded native discovery and sustained PCM acceptance")
	}
	if c.Spotify.Enabled {
		if e := c.Spotify.Helper.Validate(); e != nil {
			return nil, e
		}
	}
	if c.YouTube.Enabled {
		for _, h := range []source.Helper{c.YouTube.Resolver, c.YouTube.Runtime, c.YouTube.Decoder} {
			if e := h.Validate(); e != nil {
				return nil, e
			}
		}
		if !deploymentDecoderVersion(c.YouTube.DecoderVersion) {
			return nil, errors.New("deployment decoder must be pinned FFmpeg 9.0.2")
		}
	}
	ids, e := Prepare(c)
	if e != nil {
		return nil, e
	}
	lock, e := control.Listen(filepath.Join(c.StateDir, "runtime.sock"))
	if e != nil {
		return nil, e
	}
	r.closers = append(r.closers, lock.Close)
	target, _ := c.Volume.Target(c.Volume.Initial)
	for n, id := range ids {
		if e = PrivateDir(id.Dir); e != nil {
			return nil, e
		}
		if c.Spotify.Enabled {
			if e = r.spotify(ctx, c, id, n, co, target); e != nil {
				return nil, e
			}
		}
	}
	if c.YouTube.Enabled {
		if e = r.youtube(ctx, c, ids, co, target); e != nil {
			return nil, e
		}
	}
	return r, nil
}
func SpotifyYAML(id Identity, socket string, port, initial, zeroconfPort int) []byte {
	return []byte(fmt.Sprintf("log_level: error\ndevice_name: %s\ndevice_id: %s\nzeroconf_port: %d\naudio_backend: framed\naudio_output_pipe: %s\naudio_output_pipe_format: f32le\nexternal_volume: true\nmixer_device: ''\nvolume_steps: 100\ninitial_volume: %d\nignore_last_volume: true\ncrossfade_duration: 0\nnormalisation_disabled: true\ndisable_autoplay: true\ncache:\n  enabled: false\nmetadata:\n  enabled: false\nserver:\n  enabled: true\n  address: 127.0.0.1\n  port: %d\n", strconv.Quote(id.Group), strconv.Quote(id.DeviceID), zeroconfPort, strconv.Quote(socket), initial, port))
}
func (r *Runtime) spotify(ctx context.Context, c Config, id Identity, n int, co *source.Coordinator, target float64) error {
	dir := filepath.Join(id.Dir, "spotify")
	if e := PrivateDir(dir); e != nil {
		return e
	}
	socket := filepath.Join(dir, "pcm.sock")
	listener, e := control.Listen(socket)
	if e != nil {
		return e
	}
	r.closers = append(r.closers, listener.Close)
	instance := Nonce()
	admissionSocket := filepath.Join(dir, "admission.sock")
	admissionListener, e := control.Listen(admissionSocket)
	if e != nil {
		return e
	}
	r.closers = append(r.closers, admissionListener.Close)
	admissions := spotify.NewAdmissions(ctx, co, id.Group, instance)
	r.workers.Go(func() {
		if admissions.Serve(ctx, admissionListener) != nil && ctx.Err() == nil {
			r.fail("spotify_admission_listener", n, -1)
		}
	})
	yaml := append(SpotifyYAML(id, socket, c.Spotify.Ports[n], c.Volume.Initial, c.Spotify.ZeroconfPorts[n]), []byte(fmt.Sprintf("framed_instance: %s\nframed_admission_socket: %s\nzeroconf_interfaces_to_advertise: [%s]\n", strconv.Quote(instance), strconv.Quote(admissionSocket), strconv.Quote(c.Interface)))...)
	if e = Save(filepath.Join(dir, "config.yml"), yaml); e != nil {
		return e
	}
	process, e := source.StartDaemon(ctx, c.Spotify.Helper, spotifyArgs(dir))
	if e != nil {
		return e
	}
	r.closers = append(r.closers, process.Close)
	r.workers.Go(func() { r.watchHelper(ctx, process, n) })
	api := &spotify.API{Origin: fmt.Sprintf("http://127.0.0.1:%d", c.Spotify.Ports[n])}
	ready := time.Now().Add(10 * time.Second)
	for {
		if e = api.Volume(ctx, c.Volume.Initial); e == nil {
			break
		}
		if ctx.Err() != nil || time.Now().After(ready) {
			return errors.New("Spotify helper readiness failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	var mu sync.Mutex
	var lease source.Lease
	type observation struct {
		kind  string
		lease source.Lease
	}
	notifications := make(chan observation, 16)
	r.workers.Go(func() {
		api.Watch(ctx, func(kind string) {
			mu.Lock()
			event := observation{kind, lease}
			mu.Unlock()
			select {
			case notifications <- event:
			default:
			}
		})
	})
	r.workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-notifications:
				l := event.lease
				if event.kind == "reconcile" || event.kind == "active" || event.kind == "inactive" || event.kind == "stopped" {
					// Read actual bounded state after reconnect. Observations cannot
					// grant ownership; callbacks retain their captured generation.
					api.Reconcile(ctx, co, l)
				}
				if (event.kind == "volume" || event.kind == "reconcile") && co.Current(l) {
					v, e := api.CurrentVolume(ctx)
					if e == nil {
						co.Volume(l, v)
					}
				}
			}
		}
	})
	r.workers.Go(func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				if ctx.Err() == nil {
					r.fail("spotify_pcm_listener", n, -1)
				}
				return
			}
			input := &spotify.Input{Group: id.Group, Coordinator: co, TargetDB: target, Admissions: admissions, Lease: func(l source.Lease) {
				mu.Lock()
				lease = l
				mu.Unlock()
				v, ok := co.CommonVolume()
				if !ok {
					v = c.Volume.Initial
				}
				co.ExpectVolume(l, v)
				_ = api.Volume(ctx, v)
				if v == 0 {
					co.Mute(l, true)
				}
			}}
			_ = input.Run(ctx, conn)
			mu.Lock()
			lease = source.Lease{}
			mu.Unlock()
			if ctx.Err() != nil {
				return
			}
		}
	})
	return nil
}
func (r *Runtime) youtube(ctx context.Context, c Config, ids []Identity, co *source.Coordinator, target float64) error {
	iface, e := net.InterfaceByName(c.Interface)
	if e != nil {
		return errors.New("explicit DIAL multicast interface required")
	}
	var dials []*youtube.DIAL
	for n, id := range ids {
		dir := filepath.Join(id.Dir, "youtube")
		if e = PrivateDir(dir); e != nil {
			return e
		}
		screenPath := filepath.Join(dir, "screen-id")
		app := &youtube.NativeApp{Parent: ctx, Factory: func() (*youtube.Lounge, *youtube.PlaybackController, error) {
			screen := ""
			if _, e := os.Lstat(screenPath); e == nil {
				if e = PrivateFile(screenPath); e != nil {
					return nil, nil, e
				}
				f, e := os.Open(screenPath)
				if e != nil {
					return nil, nil, e
				}
				b, e := io.ReadAll(io.LimitReader(f, 257))
				f.Close()
				if e != nil || len(b) > 256 || strings.ContainsAny(string(b), "\r\n\x00") {
					return nil, nil, errors.New("invalid saved screen identity")
				}
				screen = string(b)
			} else if !os.IsNotExist(e) {
				return nil, nil, e
			}
			l, e := youtube.NewLounge(youtube.LoungeOptions{Name: id.Group, DeviceID: id.UUID, ScreenID: screen, Mode: c.YouTube.Mode, SaveScreen: func(s string) error { return Save(screenPath, []byte(s)) }})
			if e != nil {
				return nil, nil, e
			}
			p := youtube.NewPlaybackController(id.Group, Nonce(), co, youtube.Resolver{Helper: c.YouTube.Resolver, Runtime: c.YouTube.Runtime}, youtube.Decoder{Helper: c.YouTube.Decoder, Version: c.YouTube.DecoderVersion}, target)
			p.InitialVolume(c.Volume.Initial)
			return l, p, nil
		}}
		r.closers = append(r.closers, app.Stop)
		address := net.JoinHostPort(c.BindIP, strconv.Itoa(c.YouTube.Ports[n]))
		d, e := youtube.NewDIAL(id.Group, id.UUID, "http://"+address, app)
		if e != nil {
			return e
		}
		listener, e := net.Listen("tcp", address)
		if e != nil {
			return errors.New("DIAL listener unavailable")
		}
		server := &http.Server{Handler: d, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096}
		r.closers = append(r.closers, server.Close)
		r.workers.Go(func() {
			if server.Serve(listener) != http.ErrServerClosed && ctx.Err() == nil {
				r.fail("youtube_http_listener", n, -1)
			}
		})
		dials = append(dials, d)
	}
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	conn, e := net.ListenMulticastUDP("udp4", iface, group)
	if e != nil {
		return errors.New("DIAL multicast unavailable")
	}
	conn.SetReadBuffer(8192)
	r.closers = append(r.closers, conn.Close)
	r.workers.Go(func() {
		if youtube.ServeDiscovery(ctx, conn, dials) != nil && ctx.Err() == nil {
			r.fail("youtube_discovery", -1, -1)
		}
	})
	r.workers.Go(func() {
		notify := func(alive bool) {
			for _, d := range dials {
				conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
				_, _ = conn.WriteToUDP([]byte(d.Notify(alive)), group)
			}
		}
		notify(true)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				notify(false)
				return
			case <-ticker.C:
				notify(true)
			}
		}
	})
	return nil
}

func deploymentDecoderVersion(v string) bool { return v == "9.0.2" || v == "n9.0.2" }

func spotifyArgs(dir string) []string { return []string{"--config_dir", dir} }

func (r *Runtime) watchHelper(ctx context.Context, process *source.Process, slot int) {
	b := make([]byte, 1024)
	total := 0
	for {
		n, e := process.Read(ctx, b)
		total += n
		if e != nil || total > 4096 {
			if ctx.Err() == nil {
				class, exit := process.ExitDiagnostic()
				if total > 4096 {
					class, exit = "stdout_limit", -1
				} else if class == "running" {
					class = "read"
				}
				r.fail("spotify_"+class, slot, exit)
			}
			return
		}
	}
}
