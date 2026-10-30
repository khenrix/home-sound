package youtube

import (
	"context"
	"errors"
	"net"
	"time"
)

// ServeDiscovery has fixed memory and a global response budget. The caller owns
// multicast membership and must explicitly enable LAN advertisement.
func ServeDiscovery(ctx context.Context, c net.PacketConn, apps []*DIAL) error {
	if len(apps) != 2 {
		return errors.New("exactly two DIAL identities required")
	}
	stop := context.AfterFunc(ctx, func() { c.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, 1501)
	window := time.Now()
	count := 0
	for {
		n, peer, e := c.ReadFrom(buf)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("DIAL discovery read failed")
		}
		if n > 1500 {
			continue
		}
		host, _, e := net.SplitHostPort(peer.String())
		ip := net.ParseIP(host)
		if e != nil || ip == nil || !ip.IsPrivate() && !ip.IsLoopback() {
			continue
		}
		if time.Since(window) >= time.Second {
			count = 0
			window = time.Now()
		}
		if count >= 20 {
			continue
		}
		for _, a := range apps {
			response := a.Discovery(buf[:n])
			if response == "" {
				continue
			}
			c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			if _, e = c.WriteTo([]byte(response), peer); e != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			count++
		}
	}
}
