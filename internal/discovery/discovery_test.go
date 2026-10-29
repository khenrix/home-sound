package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAvahiResolvedAndEscaped(t *testing.T) {
	input := "+;eth0;IPv4;ignored;_airplay._tcp;local\n=;eth0;IPv4;K\\195\\182k;_airplay._tcp;local;kitchen.local;192.0.2.2;7000;\"model=Speaker\"\n=;eth0;IPv4;Cast;_googlecast._tcp;local;cast.local;192.0.2.3;8009;\n=;eth0;IPv4;evil;_airplay._tcp;local;bad;not-ip;7000;\n"
	got, err := ParseAvahi(strings.NewReader(input))
	if err != nil || len(got) != 2 || got[0].Name != "Kök" || got[1].Kind != "cast" {
		t.Fatalf("bad discovery: %+v %v", got, err)
	}
}
func TestExplicitInventoryFallback(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inventory.json")
	os.WriteFile(p, []byte(`[{"name":"LG","kind":"lg","host":"192.0.2.1","port":3000}]`), 0600)
	got, err := Discover(context.Background(), Config{Inventory: p})
	if err != nil || len(got.Devices) != 1 || got.Devices[0].Source != "inventory" || got.NetworkComplete {
		t.Fatalf("fallback misrepresented: %+v %v", got, err)
	}
}
func TestSSDPDoesNotTrustLocation(t *testing.T) {
	d, ok := parseSSDP("HTTP/1.1 200 OK\r\nSERVER: Linux/1.0 UPnP/1.0 LG WebOS TV\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\nLOCATION: http://evil.invalid/secret\r\n\r\n", "192.0.2.4")
	if !ok || d.Host != "192.0.2.4" || d.Kind != "lg" {
		t.Fatal("SSDP response not recognized")
	}
}
