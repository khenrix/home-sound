package airplay

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/webos"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("HOME_SOUND_FIXTURE")
	if mode == "" {
		return
	}
	args := os.Args
	get := func(k string) string {
		for i, a := range args {
			if a == k && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if mode == "pair" || mode == "pairfail" {
		bufio.NewReader(os.Stdin).ReadString('\n')
		if mode == "pairfail" {
			fmt.Fprintln(os.Stderr, "synthetic-secret")
			os.Exit(2)
		}
		fmt.Println("CREDENTIALS: " + strings.Repeat("a", 192))
		os.Exit(0)
	}
	if mode == "offset" && get("--clock-offset-ns") != "-123456789" {
		os.Exit(5)
	}
	if mode == "exit" {
		fmt.Println("[STATUS] error code=oops detail=synthetic-secret")
		os.Exit(2)
	}
	f, e := os.OpenFile(get("--cmdpipe"), os.O_RDWR, 0600)
	if e != nil {
		os.Exit(3)
	}
	defer f.Close()
	if get("--volume") != "0" && get("--volume") != "5" {
		os.Exit(4)
	}
	if mode == "ordering" {
		if get("--volume") != "0" {
			os.Exit(10)
		}
		p := os.Getenv("HOME_SOUND_ORDER_PATH")
		state, _ := os.ReadFile(p)
		if string(state) != "before" {
			os.Exit(8)
		}
		os.WriteFile(p, []byte("connected"), 0600)
	}
	fmt.Fprintln(os.Stderr, "[STATUS] connected")
	if mode != "cold" {
		fmt.Fprintln(os.Stderr, "[STATUS] clock_ready mode=ntp state=ready streak_ms=0 exchanges=1 ready_in_ms=0 ready_at_unix_ms=0")
	}
	if mode != "blocked" && mode != "pcm" {
		go io.Copy(io.Discard, os.Stdin)
	}
	s := bufio.NewScanner(f)
	var requested string
	pcmOK := false
	meta := []string{}
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "VOLUME=") {
			os.Exit(11)
		}
		if line == "ACTION=STOP" {
			if mode == "stopack" {
				fmt.Fprintln(os.Stderr, "[STATUS] stopped")
				time.Sleep(20 * time.Millisecond)
			}
			os.WriteFile(os.Getenv("HOME_SOUND_ORDER_PATH")+".stopped", []byte("stopped"), 0600)
			os.Exit(0)
		}
		if mode == "pcm" {
			if strings.HasPrefix(line, "TITLE=") || strings.HasPrefix(line, "ARTIST=") || strings.HasPrefix(line, "ALBUM=") {
				meta = append(meta, line)
			}
			if line == "ACTION=SENDMETA" {
				raw := make([]byte, 8)
				_, e := io.ReadFull(os.Stdin, raw)
				pcmOK = e == nil && string(raw) == string([]byte{0, 128, 255, 127, 0, 64, 0, 192}) && strings.Join(meta, "|") == "TITLE=literal=title|ARTIST=performer|ALBUM=record"
			}
		}

		if strings.HasPrefix(line, "START_UNIX_MS=") {
			requested = strings.TrimPrefix(line, "START_UNIX_MS=")
		}
		if line == "ACTION=START" {
			if mode == "ordering" {
				state, _ := os.ReadFile(os.Getenv("HOME_SOUND_ORDER_PATH"))
				if string(state) != "after" {
					os.Exit(9)
				}
			}
			if mode == "pcm" && !pcmOK {
				os.Exit(7)
			}
			if mode == "wrongack" {
				requested = "99"
			}
			v, _ := strconv.ParseInt(requested, 10, 64)
			fmt.Fprintf(os.Stderr, "[STATUS] started requested_unix_ms=%s at_unix_ms=%d\n", requested, v+100)
			if mode == "correct" {
				fmt.Fprintf(os.Stderr, "[STATUS] anchor_corrected requested_unix_ms=%s from_unix_ms=%d at_unix_ms=%d content_cut_ms=50\n", requested, v+100, v+150)
				fmt.Fprintln(os.Stderr, "[STATUS] content_cut requested_ms=50 cut_ms=50 cut_bytes=9600 drain_ms=1")
			}
		}
	}
	os.Exit(0)
}
func fixture(t *testing.T, mode string) Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper")
	exe, _ := os.Executable()
	raw := []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 HOME_SOUND_ORDER_PATH='" + p + ".order' HOME_SOUND_FIXTURE=" + mode + " exec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=TestHelperProcess -- \"$@\"\n")
	if err := os.WriteFile(p, raw, 0700); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	return Config{Helper: p, HelperSHA256: hex.EncodeToString(h[:]), Host: "127.0.0.1", Port: 7000, InitialVolume: 5}
}
func TestLifecycleAcknowledgedAnchor(t *testing.T) {
	cfg := fixture(t, "correct")
	o, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = o.Prepare(ctx, 7, 48000); err != nil {
		t.Fatal(err)
	}
	if err = o.Write(ctx, audio.Frame{Epoch: 6, Sample: 48000, Data: []float32{1, 1}}); err == nil {
		t.Fatal("accepted stale epoch")
	}
	if err = o.Write(ctx, audio.Frame{Epoch: 7, Sample: 48000, Data: []float32{.1, .1}}); err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(2000000)
	a, err := o.Start(ctx, 7, at)
	if err != nil {
		t.Fatal(err)
	}
	if a.At.UnixMilli() != 2000100 && a.At.UnixMilli() != 2000150 {
		t.Fatalf("not an acknowledgement: %+v", a)
	}
	deadline := time.Now().Add(time.Second)
	for (o.Status().Anchor.At.UnixMilli() != 2000150 || o.Status().Anchor.ContentCutPending) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	a = o.Status().Anchor
	if a.Sample != 50400 || a.At.UnixMilli() != 2000150 || !a.Corrected {
		t.Fatalf("lost sample-derived correction: %+v", a)
	}
	if err = o.Metadata(ctx, Metadata{Title: "YouTube title\nVOLUME=100"}); err == nil {
		t.Fatal("command injection")
	}
	if err = o.Metadata(ctx, Metadata{Title: "Title = literal", Artist: "Kök", Album: "Album"}); err != nil {
		t.Fatal(err)
	}
}
func TestClockNotReadyAndExit(t *testing.T) {
	for _, mode := range []string{"cold", "exit"} {
		t.Run(mode, func(t *testing.T) {
			o, _ := New(fixture(t, mode))
			defer o.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := o.Prepare(ctx, 1, 0)
			if err == nil {
				t.Fatal("prepared without clock")
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("output leaked")
			}
		})
	}
}
func TestBlockedWriteCancels(t *testing.T) {
	o, _ := New(fixture(t, "blocked"))
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := o.Prepare(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	writeCtx, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	defer stop()
	if err := o.Write(writeCtx, audio.Frame{Epoch: 1, Data: make([]float32, 48000*2)}); err == nil {
		t.Fatal("blocked input succeeded")
	}
}

type volumeFixture struct {
	calls   int
	failAt  int
	updates chan webos.Volume
}

func (v *volumeFixture) SetVolume(context.Context, int) error {
	v.calls++
	if v.calls == v.failAt {
		return fmt.Errorf("private error")
	}
	return nil
}
func (v *volumeFixture) Volume(context.Context) (webos.Volume, error) {
	return webos.Volume{Level: 10, Valid: true}, nil
}
func (v *volumeFixture) WatchVolume(context.Context) (<-chan webos.Volume, error) {
	return v.updates, nil
}
func TestLGGateAndMonitor(t *testing.T) {
	v := &volumeFixture{updates: make(chan webos.Volume, 1)}
	v.updates <- webos.Volume{Level: 10, Valid: true}
	cfg := fixture(t, "ready")
	cfg.LG = v
	cfg.InitialVolume = 0
	o, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = o.Prepare(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	if v.calls != 2 {
		t.Fatal("missing pre/post readiness volume verification")
	}
	v.updates <- webos.Volume{Level: 16, Valid: true}
	deadline := time.Now().Add(time.Second)
	for o.Status().Healthy && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o.Status().Healthy {
		t.Fatal("unsafe native volume did not stop output")
	}
	if o.Write(ctx, audio.Frame{Epoch: 1, Data: []float32{1, 1}}) == nil {
		t.Fatal("unsafe PCM released")
	}
}
func TestLGPostReadyFailure(t *testing.T) {
	v := &volumeFixture{failAt: 2, updates: make(chan webos.Volume)}
	cfg := fixture(t, "ready")
	cfg.LG = v
	cfg.InitialVolume = 0
	o, _ := New(cfg)
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if o.Prepare(ctx, 1, 0) == nil {
		t.Fatal("failed native gate accepted")
	}
	if o.Status().Healthy {
		t.Fatal("helper left healthy")
	}
}

// Catches loss of DACP identity and credential disclosure on pairing failure.
func TestPairPersistsIdentity(t *testing.T) {
	cfg := fixture(t, "pair")
	cfg.PairingFile = filepath.Join(t.TempDir(), "pair.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Pair(ctx, cfg, strings.NewReader("1234\n")); err != nil {
		t.Fatal(err)
	}
	p, err := loadCredentials(cfg.PairingFile, "127.0.0.1", 7000)
	if err != nil || len(p.DACP) != 16 || p.Auth != strings.Repeat("a", 192) {
		t.Fatal("pairing identity not retained")
	}
}
func TestPairFailureRedacted(t *testing.T) {
	cfg := fixture(t, "pairfail")
	cfg.PairingFile = filepath.Join(t.TempDir(), "pair.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := Pair(ctx, cfg, strings.NewReader("1234\n"))
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("pair failure not sanitized")
	}
	if _, err := os.Stat(cfg.PairingFile); !os.IsNotExist(err) {
		t.Fatal("persisted failed pairing")
	}
}
func TestExplicitClockOffsetArgument(t *testing.T) {
	cfg := fixture(t, "offset")
	offset := int64(-123456789)
	cfg.ClockOffsetNS = &offset
	o, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e = o.Prepare(ctx, 1, 0); e != nil {
		t.Fatal(e)
	}
}

func TestStoppedAdapterCannotPrepare(t *testing.T) {
	o, _ := New(fixture(t, "ready"))
	o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if o.Prepare(ctx, 1, 0) == nil {
		t.Fatal("stopped adapter spawned helper")
	}
}
func TestLGSubscriptionMustAcknowledgeBeforeReady(t *testing.T) {
	v := &volumeFixture{updates: make(chan webos.Volume)}
	cfg := fixture(t, "ready")
	cfg.LG = v
	cfg.InitialVolume = 0
	o, _ := New(cfg)
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if o.Prepare(ctx, 1, 0) == nil {
		t.Fatal("became ready without native subscription acknowledgement")
	}
}

// Reject mismatched acknowledgements instead of fabricating a requested-time anchor.
func TestStartRequiresMatchingAcknowledgement(t *testing.T) {
	o, _ := New(fixture(t, "wrongack"))
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e := o.Prepare(ctx, 8, 0); e != nil {
		t.Fatal(e)
	}
	ackCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, e := o.Start(ackCtx, 8, time.UnixMilli(2000000)); e == nil {
		t.Fatal("accepted unrelated START acknowledgement")
	}
}
func TestPCMAndMetadataReachHelper(t *testing.T) {
	cfg := fixture(t, "pcm")
	o, _ := New(cfg)
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e := o.Prepare(ctx, 1, 0); e != nil {
		t.Fatal(e)
	}
	if e := o.Write(ctx, audio.Frame{Epoch: 1, Data: []float32{-1, 1, 0.5, -0.5}}); e != nil {
		t.Fatal(e)
	}
	if e := o.Metadata(ctx, Metadata{Title: "literal=title", Artist: "performer", Album: "record"}); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Start(ctx, 1, time.UnixMilli(2000000)); e != nil {
		t.Fatal(e)
	}
}

func TestNativeLGOrderingOverWebSocket(t *testing.T) {
	cfg := fixture(t, "ordering")
	cfg.InitialVolume = 0
	order := cfg.Helper + ".order"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		reads := 0
		for {
			var m map[string]any
			if wsjson.Read(ctx, c, &m) != nil {
				return
			}
			p := map[string]any{"returnValue": true}
			typ := "response"
			switch m["type"] {
			case "register":
				typ = "registered"
				p = map[string]any{"client-key": "fixture-key"}
			default:
				switch m["uri"] {
				case "ssap://audio/setVolume":
					if m["payload"].(map[string]any)["volume"] != float64(10) {
						t.Error("wrong TV volume")
					}
				case "ssap://audio/getVolume":
					if m["type"] != "subscribe" {
						reads++
						if reads == 1 {
							os.WriteFile(order, []byte("before"), 0600)
						} else {
							state, _ := os.ReadFile(order)
							if string(state) != "connected" {
								t.Error("post-volume check before helper readiness")
							}
							os.WriteFile(order, []byte("after"), 0600)
						}
					}
					p["volume"] = 10
					p["muted"] = false
				default:
					t.Error("unexpected control")
				}
			}
			if wsjson.Write(ctx, c, map[string]any{"id": m["id"], "type": typ, "payload": p}) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wc := webos.Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), PairingFile: filepath.Join(t.TempDir(), "lg.json")}
	c, e := webos.Connect(ctx, wc, true)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	cfg.LG = c
	o, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer o.Stop()
	if e = o.Prepare(ctx, 2, 0); e != nil {
		t.Fatal(e)
	}
	if e = o.Write(ctx, audio.Frame{Epoch: 2, Data: []float32{.1, .1}}); e != nil {
		t.Fatal(e)
	}
	if _, e = o.Start(ctx, 2, time.UnixMilli(2000000)); e != nil {
		t.Fatal(e)
	}
}

func TestCorrectedAnchorUsesActualContentCut(t *testing.T) {
	o, _ := New(fixture(t, "ready"))
	o.mu.Lock()
	o.pending = 1000
	o.status.Anchor = Anchor{Epoch: 1, Sample: 48000, Requested: time.UnixMilli(1000), At: time.UnixMilli(1100)}
	o.mu.Unlock()
	o.consume("[STATUS] anchor_corrected requested_unix_ms=1000 from_unix_ms=1100 at_unix_ms=1150 content_cut_ms=50")
	if got := o.Status().Anchor; got.Sample != 48000 {
		t.Fatal("claimed requested cut as actual discarded PCM")
	}
	o.consume("[STATUS] content_cut requested_ms=50 cut_ms=20 cut_bytes=3840 drain_ms=15")
	if got := o.Status().Anchor; got.Sample != 48960 {
		t.Fatalf("did not use actual dropped frames: %+v", got)
	}
}

func TestRejectInvalidConfiguration(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.InitialVolume = 16 }, func(c *Config) { c.InitialVolume = 0 }, func(c *Config) { c.Host = "::1" }, func(c *Config) { c.HelperSHA256 = strings.Repeat("0", 64) }} {
		cfg := fixture(t, "ready")
		change(&cfg)
		if _, e := New(cfg); e == nil {
			t.Fatal("accepted unsupported or unsafe configuration")
		}
	}
}
func TestStopRequestsReceiverTeardown(t *testing.T) {
	cfg := fixture(t, "stopack")
	o, _ := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e := o.Prepare(ctx, 1, 0); e != nil {
		t.Fatal(e)
	}
	if e := o.Stop(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(cfg.Helper + ".order.stopped"); e != nil {
		t.Fatal("helper was killed without receiver STOP")
	}
}
