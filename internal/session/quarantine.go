package session

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/khenrix/home-sound/internal/config"
)

type unresolvedTeardown struct {
	id     string
	keys   []string
	output Output
}

// Native volume belongs to the physical host, not its profile, adapter name or
// service port. Keep the configured ID too, including devices without an endpoint configuration.
func deviceKeys(id string, d config.Device) []string {
	keys := []string{"id:" + id}
	hosts := []string{d.Host}
	if u, err := url.Parse(d.LGURL); err == nil {
		hosts = append(hosts, u.Hostname())
	}
	for _, host := range hosts {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if ip := net.ParseIP(host); ip != nil {
			host = ip.String()
		}
		if host != "" && !slices.Contains(keys, "host:"+host) {
			keys = append(keys, "host:"+host)
		}
	}
	return keys
}
func sameDevice(a, b []string) bool {
	for _, key := range a {
		if slices.Contains(b, key) {
			return true
		}
	}
	return false
}
func (e *Engine) quarantineOutput(id string, d config.Device, out Output) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.quarantine = append(e.quarantine, &unresolvedTeardown{id: id, keys: deviceKeys(id, d), output: out})
}
func (e *Engine) isQuarantined(id string, d config.Device) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, q := range e.quarantine {
		if sameDevice(q.keys, deviceKeys(id, d)) {
			return true
		}
	}
	return false
}
func (e *Engine) quarantineStatus() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for _, q := range e.quarantine {
		if !slices.Contains(ids, q.id) {
			ids = append(ids, q.id)
		}
	}
	slices.Sort(ids)
	return ids
}

// Called under op, after the old session has joined. Only another successful
// Stop on the original output can release its fence; a new generation cannot.
func (e *Engine) resolveTeardown(ids []string) error {
	e.mu.Lock()
	pending := append([]*unresolvedTeardown(nil), e.quarantine...)
	e.mu.Unlock()
	for _, q := range pending {
		selected := false
		for _, id := range ids {
			selected = selected || sameDevice(q.keys, deviceKeys(id, e.cfg.Devices[id]))
		}
		if !selected {
			continue
		}
		if err := q.output.Stop(); err != nil {
			return errors.New("device quarantined: receiver teardown remains unresolved")
		}
		e.mu.Lock()
		e.quarantine = slices.DeleteFunc(e.quarantine, func(v *unresolvedTeardown) bool { return v == q })
		e.mu.Unlock()
	}
	return nil
}
