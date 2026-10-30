package youtube

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDiscoverySocketReturnsBothDistinctIdentities(t *testing.T) {
	conn, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	a, _ := NewDIAL("All Speakers", "11111111-1111-1111-1111-111111111111", "http://127.0.0.1:38001", &dialApp{})
	b, _ := NewDIAL("TV + All Speakers", "22222222-2222-2222-2222-222222222222", "http://127.0.0.1:38002", &dialApp{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeDiscovery(ctx, conn, []*DIAL{a, b}) }()
	client, e := net.Dial("udp4", conn.LocalAddr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(200 * time.Millisecond))
	client.Write([]byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:dial-multiscreen-org:service:dial:1\r\n\r\n"))
	seen := map[string]bool{}
	for range 2 {
		buf := make([]byte, 2048)
		n, e := client.Read(buf)
		if e != nil {
			t.Fatalf("native DIAL socket produced no response: %v", e)
		}
		seen[string(buf[:n])] = true
	}
	if len(seen) != 2 {
		t.Fatal("duplicate discovery identity")
	}
	for s := range seen {
		if !strings.Contains(s, "LOCATION: http://127.0.0.1:") {
			t.Fatal(s)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("discovery shutdown blocked")
	}
}
