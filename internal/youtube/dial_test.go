package youtube

import (
	"context"
	"github.com/khenrix/home-sound/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type dialApp struct {
	code    string
	running bool
}

func (a *dialApp) Launch(_ context.Context, code string) error {
	a.code = code
	a.running = true
	return nil
}
func (a *dialApp) Stop() error   { a.running = false; return nil }
func (a *dialApp) Running() bool { return a.running }
func TestDIALBothNamesUSNsLaunchStop(t *testing.T) {
	for i, name := range []string{config.TVAndSpeakers, config.AllSpeakers} {
		app := &dialApp{}
		uuid := []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"}[i]
		d, e := NewDIAL(name, uuid, "http://127.0.0.1:8008", app)
		if e != nil {
			t.Fatal(e)
		}
		w := httptest.NewRecorder()
		d.ServeHTTP(w, httptest.NewRequest("GET", "http://local/description.xml", nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), name) || w.Header().Get("Application-URL") != "http://127.0.0.1:8008/apps/" {
			t.Fatalf("description %d %s %v", w.Code, w.Body, w.Header())
		}
		packet := []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:dial-multiscreen-org:service:dial:1\r\n\r\n")
		response := d.Discovery(packet)
		if !strings.Contains(response, "USN: uuid:"+uuid+"::urn:dial-multiscreen-org:service:dial:1") || !strings.Contains(response, "LOCATION: http://127.0.0.1:8008/description.xml") {
			t.Fatalf("discovery %q", response)
		}
		r := httptest.NewRequest("POST", "http://local/apps/YouTube", strings.NewReader("pairingCode=fixture-code&theme=cl"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w = httptest.NewRecorder()
		d.ServeHTTP(w, r)
		if w.Result().StatusCode != 201 || w.Result().Header.Get("Location") != "http://127.0.0.1:8008/apps/YouTube/run" || app.code != "fixture-code" {
			t.Fatalf("launch %d %v %s", w.Code, w.Result().Header, app.code)
		}
		w = httptest.NewRecorder()
		d.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "http://local/apps/YouTube/run", nil))
		if w.Code != 200 || app.running {
			t.Fatalf("stop %d", w.Code)
		}
	}
}
func TestDIALRejectMalformedLaunch(t *testing.T) {
	d, _ := NewDIAL(config.AllSpeakers, "00000000-0000-0000-0000-000000000001", "http://127.0.0.1:8008", &dialApp{})
	for _, body := range []string{"theme=cl", "pairingCode=x&theme=wrong", "pairingCode=x&pairingCode=y&theme=cl", strings.Repeat("x", 3000)} {
		r := httptest.NewRequest("POST", "http://local/apps/YouTube", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		d.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("accepted launch %q", body)
		}
	}
	if d.Discovery([]byte("M-SEARCH junk")) != "" {
		t.Fatal("accepted malformed SSDP")
	}
}

// Chromium DialURLFetcher::Start attaches URL-encoded launch data with
// AttachStringForUpload(..., "text/plain"), rather than a form MIME type.
func TestDIALChromiumPlainTextLaunch(t *testing.T) {
	for _, contentType := range []string{"text/plain", "text/plain; charset=UTF-8", "application/x-www-form-urlencoded"} {
		t.Run(contentType, func(t *testing.T) {
			app := &dialApp{}
			d, e := NewDIAL(config.AllSpeakers, "00000000-0000-0000-0000-000000000001", "http://127.0.0.1:8008", app)
			if e != nil {
				t.Fatal(e)
			}
			r := httptest.NewRequest(http.MethodPost, "http://local/apps/YouTube", strings.NewReader("pairingCode=fixture%2Dcode&theme=cl"))
			r.Header.Set("Content-Type", contentType)
			w := httptest.NewRecorder()
			d.ServeHTTP(w, r)
			if w.Code != http.StatusCreated || w.Header().Get("Location") != "http://127.0.0.1:8008/apps/YouTube/run" || !app.Running() || app.code != "fixture-code" {
				t.Fatalf("native launch was not admitted: status=%d running=%t", w.Code, app.Running())
			}
		})
	}
}

func TestDIALPlainTextRetainsLaunchValidation(t *testing.T) {
	for _, tc := range []struct{ contentType, body string }{
		{"text/plain", "theme=cl"},
		{"text/plain; charset=UTF-8", "pairingCode=x&theme=wrong"},
		{"text/plain", "pairingCode=x&pairingCode=y&theme=cl"},
		{"text/plain", "pairingCode=x&theme=cl&theme=cl"},
		{"text/plain", "pairingCode=%ff&theme=cl"},
		{"text/plain", "pairingCode=bad%0Avalue&theme=cl"},
		{"text/plain", "pairingCode=x&theme=cl&padding=" + strings.Repeat("x", 2048)},
		{"application/json", "pairingCode=x&theme=cl"},
		{"text/plain; charset=", "pairingCode=x&theme=cl"},
	} {
		app := &dialApp{}
		d, _ := NewDIAL(config.AllSpeakers, "00000000-0000-0000-0000-000000000001", "http://127.0.0.1:8008", app)
		r := httptest.NewRequest(http.MethodPost, "http://local/apps/YouTube", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		d.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || app.Running() {
			t.Errorf("malformed launch admitted: status=%d", w.Code)
		}
	}
}
