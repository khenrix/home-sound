package airplay

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/khenrix/home-sound/internal/pairing"
)

type credentials struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Auth string `json:"auth"`
	DACP string `json:"dacp"`
}

func validCredentials(p credentials) bool {
	auth, e := hex.DecodeString(p.Auth)
	id, e2 := hex.DecodeString(p.DACP)
	return e == nil && len(auth) == 96 && e2 == nil && len(id) == 8
}
func loadCredentials(path, host string, port int) (credentials, error) {
	var p credentials
	if pairing.Load(path, &p) != nil || p.Host != host || p.Port != port || !validCredentials(p) {
		return credentials{}, errors.New("valid AirPlay pairing required")
	}
	return p, nil
}

type boundedCapture struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	n := len(p)
	if n > 16384-b.Len() {
		b.overflow = true
		p = p[:16384-b.Len()]
	}
	b.Buffer.Write(p)
	return n, nil
}

// Pair consumes the PIN from a local input reader. The pinned helper only accepts
// stored auth through argv; that limitation remains inside the child boundary.
func Pair(ctx context.Context, cfg Config, pin io.Reader) error {
	if !filepath.IsAbs(cfg.Helper) || net.ParseIP(cfg.Host).To4() == nil || cfg.Port < 1 || cfg.Port > 65535 || cfg.PairingFile == "" || pin == nil {
		return errors.New("pairing requires helper, IPv4, port, private file and local input")
	}
	if err := verifyHelper(cfg.Helper, cfg.HelperSHA256); err != nil {
		return err
	}
	var identity [8]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return errors.New("cannot create pairing identity")
	}
	dacp := strings.ToUpper(hex.EncodeToString(identity[:]))
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Helper, "--pair-setup", "--port", strconv.Itoa(cfg.Port), "--dacp", dacp, "--debug", "0", cfg.Host)
	cmd.Stdin = pin
	cmd.Stderr = io.Discard
	var captured boundedCapture
	cmd.Stdout = &captured
	cmd.WaitDelay = 100 * time.Millisecond
	if cmd.Run() != nil || captured.overflow {
		return errors.New("AirPlay pairing failed")
	}
	p := credentials{Host: cfg.Host, Port: cfg.Port, DACP: dacp}
	s := bufio.NewScanner(&captured.Buffer)
	for s.Scan() {
		if value, ok := strings.CutPrefix(s.Text(), "CREDENTIALS: "); ok {
			if p.Auth != "" {
				return errors.New("invalid pairing response")
			}
			p.Auth = value
		}
	}
	if s.Err() != nil || !validCredentials(p) {
		return errors.New("invalid pairing response")
	}
	return pairing.Save(cfg.PairingFile, p)
}
