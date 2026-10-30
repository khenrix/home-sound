package airplay

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/khenrix/home-sound/internal/audio"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func runPTPFixture(get func(string) string) {
	name := get("--ptp-shm-name")
	port := get("--ptp-control-port")
	if !strings.HasPrefix(name, "/hs-") || get("--clock-offset-ns") != "123" {
		os.Exit(40)
	}
	path := "/dev/shm" + name
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		os.Exit(41)
	}
	addr, e := net.ResolveUDPAddr("udp4", "127.0.0.1:"+port)
	if e != nil {
		os.Exit(42)
	}
	sock, e := net.ListenUDP("udp4", addr)
	if e != nil {
		os.Exit(43)
	}
	events := os.Getenv("HOME_SOUND_ORDER_PATH") + ".daemon"
	os.WriteFile(events, []byte(fmt.Sprintf("%d %s", os.Getpid(), name)), 0600)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	go func() { <-stop; sock.Close(); f.Close(); os.Remove(path); os.Exit(0) }()
	var count uint64
	go func() {
		for {
			if _, err := os.Stat(events + ".clockhang"); err == nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			count++
			raw := make([]byte, 3176)
			binary.NativeEndian.PutUint32(raw, 4)
			binary.NativeEndian.PutUint64(raw[8:], count)
			copy(raw[1592:], raw[8:1592])
			f.WriteAt(raw, 0)
			time.Sleep(10 * time.Millisecond)
		}
	}()
	buf := make([]byte, 256)
	for {
		n, peer, e := sock.ReadFromUDP(buf)
		if e != nil {
			os.Exit(0)
		}
		if string(buf[:n]) == "?" {
			if _, e := os.Stat(events + ".hang"); e == nil {
				continue
			}
			sock.WriteToUDP([]byte("OK peers=0 gm=0123456789abcdef role=grandmaster"), peer)
		}
	}
}
func TestPTPRequiresFrozenMapping(t *testing.T) {
	cfg := fixture(t, "ready")
	cfg.Transport = Transport{Rate: 44100, Timing: "ptp"}
	if _, err := New(cfg); err == nil {
		t.Fatal("PTP accepted no frozen host-clock mapping")
	}
}
func TestOwnedPTPLeaseLifecycleAndHungControl(t *testing.T) {
	cfg := fixture(t, "ready")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	m := &ptpManager{cadence: 100 * time.Millisecond, timeout: 40 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	one, err := m.acquire(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.acquire(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if one.daemon != two.daemon {
		t.Fatal("two daemon processes for concurrent outputs")
	}
	if err = one.release(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-two.failed():
		t.Fatal("first release terminated remaining lease")
	default:
	}
	other := cfg
	different := int64(124)
	other.ClockOffsetNS = &different
	if _, err = m.acquire(ctx, other); err == nil {
		t.Fatal("mixed frozen mappings")
	}
	if err = os.WriteFile(cfg.Helper+".order.daemon.hang", nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-two.failed():
	case <-ctx.Done():
		t.Fatal("hung control channel remained healthy")
	}
	if err = two.release(); err != nil {
		t.Fatal(err)
	}
	os.Remove(cfg.Helper + ".order.daemon.hang")
	next, err := m.acquire(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.daemon.name == two.daemon.name {
		t.Fatal("restart reused possibly orphaned namespace")
	}
	if err = next.release(); err != nil {
		t.Fatal(err)
	}
}
func TestPTPDeathAndStartupFailure(t *testing.T) {
	cfg := fixture(t, "ready")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	m := &ptpManager{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lease, err := m.acquire(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, e := os.ReadFile(cfg.Helper + ".order.daemon")
	if e != nil {
		t.Fatal(e)
	}
	parts := strings.Fields(string(raw))
	pid, _ := strconv.Atoi(parts[0])
	defer os.Remove("/dev/shm" + parts[1]) // fixture owns this deliberate crash orphan
	syscall.Kill(pid, syscall.SIGKILL)
	select {
	case <-lease.failed():
	case <-ctx.Done():
		t.Fatal("daemon death not detected")
	}
	lease.release()
	cfg.HelperSHA256 = strings.Repeat("0", 64)
	if _, err = m.acquire(ctx, cfg); err == nil {
		t.Fatal("unverified daemon launched")
	}
}

func TestPTPPrepareRequiresSharedClockAndClosesAfterLoss(t *testing.T) {
	cfg := fixture(t, "ptp_stream")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	cfg.Transport = Transport{Rate: 44100, Timing: "ptp"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = a.Prepare(ctx, 1, 0); err != nil {
		t.Fatalf("shared measured PTP did not prepare: %v", err)
	}
	if !a.Status().ClockReady || !a.Status().ClockMeasurementKnown {
		t.Fatal("PTP was not measured")
	}
	if err = a.Write(ctx, audio.Frame{Epoch: 1, Data: make([]float32, 1920)}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.Helper+".order.daemon.hang", nil, 0600); err != nil {
		t.Fatal(err)
	}
	for a.Status().Healthy && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if a.Status().Healthy {
		t.Fatal("hung PTP retained readiness")
	}
	if err = a.Write(ctx, audio.Frame{Epoch: 1, Data: []float32{.1, .1}}); err == nil {
		t.Fatal("PCM accepted after daemon failure")
	}
	if _, err = a.Start(ctx, 1, time.UnixMilli(2000000)); err == nil {
		t.Fatal("START accepted after daemon failure")
	}
}

func TestPTPConcurrentLeasesShareOneProcess(t *testing.T) {
	cfg := fixture(t, "ready")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	m := &ptpManager{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		lease *ptpLease
		err   error
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		go func() { l, e := m.acquire(ctx, cfg); results <- result{l, e} }()
	}
	var leases []*ptpLease
	for i := 0; i < 8; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		leases = append(leases, r.lease)
	}
	for _, l := range leases {
		if l.daemon != leases[0].daemon {
			t.Fatal("parallel callers started separate daemons")
		}
	}
	for _, l := range leases {
		if err := l.release(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-leases[0].daemon.done:
	default:
		t.Fatal("last release did not reap owned daemon")
	}
}
func TestPTPFreshControlCannotHideStalledClock(t *testing.T) {
	cfg := fixture(t, "ready")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	m := &ptpManager{cadence: 100 * time.Millisecond, timeout: 40 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	l, err := m.acquire(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	if err = os.WriteFile(cfg.Helper+".order.daemon.clockhang", nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.failed():
	case <-ctx.Done():
		t.Fatal("fresh control replies hid stalled shared clock")
	}
}
func TestPTPStartupFailureIsBounded(t *testing.T) {
	cfg := fixture(t, "ptp_fail")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	m := &ptpManager{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.acquire(ctx, cfg); err == nil {
		t.Fatal("exited daemon yielded a live lease")
	}
	if m.active != nil {
		t.Fatal("failed startup retained singleton")
	}
}
func TestRequiredPTPRejectsColdProbingStalledAndNTPFallback(t *testing.T) {
	for _, mode := range []string{"ptp_cold", "ptp_probing", "ptp_stalled", "ntp_cold"} {
		t.Run(mode, func(t *testing.T) {
			cfg := fixture(t, mode)
			offset := int64(123)
			cfg.ClockOffsetNS = &offset
			cfg.Transport = Transport{Rate: 44100, Timing: "ptp"}
			a, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if a.Prepare(ctx, 1, 0) == nil {
				t.Fatal("unmeasured/fallback PTP prepared")
			}
			if a.Status().ClockState == "" {
				t.Fatal("fixture did not reach clock report")
			}
			if a.Status().Ready {
				t.Fatal("unsafe readiness")
			}
		})
	}
}

func TestStoppedOutputDoesNotReportReapedDaemonHealthy(t *testing.T) {
	cfg := fixture(t, "ptp_stream")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	cfg.Transport = Transport{Rate: 44100, Timing: "ptp"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = a.Prepare(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err = a.Stop(); err != nil {
		t.Fatal(err)
	}
	if a.Status().PTPDaemonHealthy {
		t.Fatal("reaped daemon still reported healthy")
	}
}
