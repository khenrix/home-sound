package airplay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var sharedPTP ptpManager

type ptpManager struct {
	mu               sync.Mutex
	active           *ptpDaemon
	cadence, timeout time.Duration
}
type ptpDaemon struct {
	helper, checksum, name string
	offset                 int64
	port                   int
	refs                   int
	cmd                    *exec.Cmd
	cancel                 context.CancelFunc
	lost, done             chan struct{}
	healthy                atomic.Bool
	once                   sync.Once
}
type ptpLease struct {
	manager *ptpManager
	daemon  *ptpDaemon
	once    sync.Once
	err     error
}

func (l *ptpLease) failed() <-chan struct{} { return l.daemon.lost }
func (d *ptpDaemon) fail()                  { d.healthy.Store(false); d.once.Do(func() { close(d.lost) }); d.cancel() }
func (m *ptpManager) acquire(ctx context.Context, cfg Config) (*ptpLease, error) {
	if cfg.ClockOffsetNS == nil {
		return nil, errors.New("shared PTP requires frozen clock mapping")
	}
	if err := verifyHelper(cfg.Helper, cfg.HelperSHA256); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d := m.active; d != nil {
		if !d.healthy.Load() || d.helper != cfg.Helper || d.checksum != cfg.HelperSHA256 || d.offset != *cfg.ClockOffsetNS {
			return nil, errors.New("shared PTP unavailable or clock/helper mismatch")
		}
		d.refs++
		return &ptpLease{manager: m, daemon: d}, nil
	}
	var random [10]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, errors.New("cannot allocate PTP identity")
	}
	reservation, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, errors.New("cannot reserve PTP control endpoint")
	}
	port := reservation.LocalAddr().(*net.UDPAddr).Port
	reservation.Close()
	life, cancel := context.WithCancel(context.Background())
	d := &ptpDaemon{helper: cfg.Helper, checksum: cfg.HelperSHA256, name: "/hs-ptp-" + hex.EncodeToString(random[:]), offset: *cfg.ClockOffsetNS, port: port, refs: 1, cancel: cancel, lost: make(chan struct{}), done: make(chan struct{})}
	args := []string{"--ptp-daemon", "--ptp-shm-name", d.name, "--ptp-control-port", strconv.Itoa(port), "--clock-offset-ns", strconv.FormatInt(d.offset, 10), "--debug", "0"}
	d.cmd = exec.CommandContext(life, cfg.Helper, args...)
	if err := ownedPTPProcess(d.cmd); err != nil {
		cancel()
		return nil, err
	}
	d.cmd.Stdout = io.Discard
	d.cmd.Stderr = io.Discard
	d.cmd.Cancel = func() error { return d.cmd.Process.Signal(syscall.SIGTERM) }
	d.cmd.WaitDelay = time.Second
	if d.cmd.Start() != nil {
		cancel()
		return nil, errors.New("cannot start owned PTP daemon")
	}
	d.healthy.Store(true)
	go func() { d.cmd.Wait(); d.fail(); close(d.done) }()
	timeout := m.timeout
	if timeout == 0 {
		timeout = time.Second
	}
	startup, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	var sequence uint64
	for {
		sequence, err = probePTP(startup, d, timeout)
		if err == nil {
			break
		}
		select {
		case <-startup.Done():
			d.fail()
			<-d.done
			return nil, errors.New("owned PTP startup timed out")
		case <-d.done:
			return nil, errors.New("owned PTP daemon exited")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case <-d.done:
		return nil, errors.New("owned PTP daemon exited")
	default:
	}
	if !d.healthy.Load() {
		return nil, errors.New("owned PTP daemon exited")
	}
	m.active = d
	cadence := m.cadence
	if cadence == 0 {
		cadence = time.Second
	}
	go func() {
		ticker := time.NewTicker(cadence)
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-ticker.C:
				next, err := probePTP(life, d, timeout)
				if err != nil || next <= sequence {
					d.fail()
					return
				}
				sequence = next
			}
		}
	}()
	return &ptpLease{manager: m, daemon: d}, nil
}
func (l *ptpLease) release() error {
	l.once.Do(func() {
		m, d := l.manager, l.daemon
		m.mu.Lock()
		defer m.mu.Unlock()
		d.refs--
		if d.refs != 0 {
			return
		}
		d.fail()
		select {
		case <-d.done:
		case <-time.After(3 * time.Second):
			l.err = errors.New("owned PTP teardown timed out")
		}
		if m.active == d {
			m.active = nil
		}
	})
	return l.err
}
func probePTP(parent context.Context, d *ptpDaemon, timeout time.Duration) (uint64, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(d.port)))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = conn.Write([]byte("?")); err != nil {
		return 0, err
	}
	raw := make([]byte, 256)
	n, err := conn.Read(raw)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw[:n]))
	if len(fields) != 4 || fields[0] != "OK" || !strings.HasPrefix(fields[1], "peers=") || !strings.HasPrefix(fields[2], "gm=") || (fields[3] != "role=grandmaster" && fields[3] != "role=slave") {
		return 0, errors.New("invalid PTP control reply")
	}
	peers, e := strconv.Atoi(strings.TrimPrefix(fields[1], "peers="))
	if e != nil || peers < 0 || peers > 64 {
		return 0, errors.New("invalid PTP peer count")
	}
	gm := strings.TrimPrefix(fields[2], "gm=")
	if len(gm) != 16 {
		return 0, errors.New("invalid PTP identity")
	}
	if _, e = hex.DecodeString(gm); e != nil {
		return 0, errors.New("invalid PTP identity")
	}
	f, err := os.Open("/dev/shm" + d.name)
	if err != nil {
		return 0, errors.New("owned PTP clock unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 3177))
	if err != nil || len(data) != 3176 || binary.NativeEndian.Uint32(data) != 4 || !bytes.Equal(data[8:1592], data[1592:]) {
		return 0, errors.New("owned PTP clock inconsistent")
	}
	sequence := binary.NativeEndian.Uint64(data[8:])
	if sequence == 0 {
		return 0, errors.New("owned PTP clock unpublished")
	}
	return sequence, nil
}
