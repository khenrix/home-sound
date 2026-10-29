// Package discovery browses advertisements without connecting to device controls.
package discovery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Device struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Source string `json:"source,omitempty"`
}
type Config struct {
	AvahiHelper, Inventory string
	Network                bool
}
type Result struct {
	Devices         []Device `json:"devices"`
	NetworkComplete bool     `json:"network_complete"`
	Warnings        []string `json:"warnings,omitempty"`
}

func valid(d Device) bool {
	return d.Name != "" && net.ParseIP(d.Host) != nil && d.Port > 0 && d.Port < 65536 && (d.Kind == "airplay" || d.Kind == "cast" || d.Kind == "lg")
}
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, e := strconv.ParseUint(s[i+1:i+4], 10, 8); e == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
func ParseAvahi(r io.Reader) ([]Device, error) {
	var out []Device
	s := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for s.Scan() {
		f := strings.SplitN(s.Text(), ";", 10)
		if len(f) < 9 || f[0] != "=" {
			continue
		}
		kind := ""
		switch f[4] {
		case "_airplay._tcp", "_raop._tcp":
			kind = "airplay"
		case "_googlecast._tcp":
			kind = "cast"
		default:
			continue
		}
		port, _ := strconv.Atoi(f[8])
		d := Device{unescape(f[3]), kind, f[7], port, "mdns"}
		if valid(d) {
			out = append(out, d)
		}
	}
	return out, s.Err()
}

type cappedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n > (1<<20)-b.Len() {
		p = p[:(1<<20)-b.Len()]
		b.overflow = true
	}
	b.Buffer.Write(p)
	return n, nil
}
func Discover(ctx context.Context, cfg Config) (Result, error) {
	result := Result{Devices: []Device{}}
	seen := map[string]bool{}
	add := func(ds []Device) {
		for _, d := range ds {
			k := d.Kind + "/" + net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
			if !seen[k] {
				seen[k] = true
				result.Devices = append(result.Devices, d)
			}
		}
	}
	if cfg.Inventory != "" {
		f, e := os.Open(cfg.Inventory)
		if e != nil {
			return result, errors.New("cannot read inventory")
		}
		var ds []Device
		dec := json.NewDecoder(io.LimitReader(f, 1<<20))
		dec.DisallowUnknownFields()
		e = dec.Decode(&ds)
		var extra any
		trailing := dec.Decode(&extra)
		f.Close()
		if e != nil || trailing != io.EOF {
			return result, errors.New("invalid inventory")
		}
		for i := range ds {
			if !valid(ds[i]) {
				return result, errors.New("invalid inventory device")
			}
			ds[i].Source = "inventory"
		}
		add(ds)
	}
	if !cfg.Network {
		if cfg.Inventory == "" {
			return result, errors.New("explicit inventory or network discovery required")
		}
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result.NetworkComplete = true
	if !filepath.IsAbs(cfg.AvahiHelper) {
		result.NetworkComplete = false
		result.Warnings = append(result.Warnings, "mDNS unavailable: absolute avahi-browse path required")
	} else {
		for _, service := range []string{"_airplay._tcp", "_raop._tcp", "_googlecast._tcp"} {
			cmd := exec.CommandContext(ctx, cfg.AvahiHelper, "--resolve", "--terminate", "--parsable", "--no-db-lookup", service)
			var b cappedBuffer
			cmd.Stdout = &b
			cmd.Stderr = io.Discard
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			ds, parseErr := ParseAvahi(&b.Buffer)
			add(ds)
			if err != nil || parseErr != nil || b.overflow {
				result.NetworkComplete = false
			}
		}
		if !result.NetworkComplete {
			result.Warnings = append(result.Warnings, "mDNS incomplete; inventory remains available")
		}
	}
	ds, err := discoverLG(ctx)
	add(ds)
	if err != nil {
		result.NetworkComplete = false
		result.Warnings = append(result.Warnings, "LG SSDP incomplete")
	}
	return result, nil
}
func parseSSDP(raw, host string) (Device, bool) {
	r, e := http.ReadResponse(bufio.NewReader(strings.NewReader(raw)), nil)
	if e != nil {
		return Device{}, false
	}
	defer r.Body.Close()
	server := strings.ToLower(r.Header.Get("Server"))
	if r.StatusCode != 200 || (!strings.Contains(server, "lg") && !strings.Contains(server, "webos")) || r.Header.Get("ST") != "urn:schemas-upnp-org:device:MediaRenderer:1" {
		return Device{}, false
	}
	d := Device{"LG webOS", "lg", host, 3000, "ssdp"}
	return d, valid(d)
}
func discoverLG(ctx context.Context) ([]Device, error) {
	conn, e := net.ListenUDP("udp4", &net.UDPAddr{})
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	query := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"
	if _, e = conn.WriteToUDP([]byte(query), &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}); e != nil {
		return nil, e
	}
	var out []Device
	buf := make([]byte, 8192)
	for {
		n, addr, e := conn.ReadFromUDP(buf)
		if e != nil {
			if ne, ok := e.(net.Error); ok && ne.Timeout() && ctx.Err() == nil {
				return out, nil
			}
			return out, e
		}
		if d, ok := parseSSDP(string(buf[:n]), addr.IP.String()); ok {
			out = append(out, d)
		}
	}
}
