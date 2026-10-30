package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/khenrix/home-sound/internal/config"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const dialService = "urn:dial-multiscreen-org:service:dial:1"

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var pairingPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

type App interface {
	Launch(context.Context, string) error
	Stop() error
	Running() bool
}
type DIAL struct {
	name, uuid, base string
	app              App
}

func NewDIAL(name, uuid, base string, app App) (*DIAL, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("explicit DIAL HTTP origin required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsPrivate() && !ip.IsLoopback() {
		return nil, errors.New("DIAL origin must use a local IP")
	}
	if name != config.AllSpeakers && name != config.TVAndSpeakers || !uuidPattern.MatchString(uuid) || app == nil {
		return nil, errors.New("invalid DIAL identity")
	}
	return &DIAL{name, uuid, base, app}, nil
}
func (d *DIAL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "GET /description.xml":
		w.Header().Set("Application-URL", d.base+"/apps/")
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		var name bytes.Buffer
		xml.EscapeText(&name, []byte(d.name))
		fmt.Fprintf(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><specVersion><major>1</major><minor>0</minor></specVersion><device><deviceType>urn:dial-multiscreen-org:device:dial:1</deviceType><friendlyName>%s</friendlyName><manufacturer>Home Sound</manufacturer><modelName>Home Sound Audio</modelName><UDN>uuid:%s</UDN><serviceList><service><serviceType>%s</serviceType><serviceId>urn:dial-multiscreen-org:serviceId:dial</serviceId><SCPDURL>/dial.xml</SCPDURL><controlURL>/apps/</controlURL><eventSubURL></eventSubURL></service></serviceList></device></root>`, name.String(), d.uuid, dialService)
	case "GET /apps/YouTube":
		state, link := "stopped", ""
		if d.app.Running() {
			state = "running"
			link = `<link rel="run" href="run"/>`
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		fmt.Fprintf(w, `<?xml version="1.0"?><service xmlns="urn:dial-multiscreen-org:schemas:dial" dialVer="1.7"><name>YouTube</name><options allowStop="true"/><state>%s</state>%s</service>`, state, link)
	case "POST /apps/YouTube":
		content, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || content != "application/x-www-form-urlencoded" && content != "text/plain" {
			http.Error(w, "invalid launch content type", 400)
			return
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, 2049))
		if e != nil || len(b) > 2048 {
			http.Error(w, "launch too large", 400)
			return
		}
		v, e := url.ParseQuery(string(b))
		if e != nil || len(v["pairingCode"]) != 1 || !pairingPattern.MatchString(v.Get("pairingCode")) || len(v["theme"]) != 1 || v.Get("theme") != "cl" {
			http.Error(w, "invalid YouTube launch", 400)
			return
		}
		if e = d.app.Launch(r.Context(), v.Get("pairingCode")); e != nil {
			http.Error(w, "YouTube launch failed", 503)
			return
		}
		w.Header().Set("Location", d.base+"/apps/YouTube/run")
		w.WriteHeader(201)
	case "DELETE /apps/YouTube/run":
		if d.app.Stop() != nil {
			http.Error(w, "YouTube stop failed", 503)
			return
		}
		w.WriteHeader(200)
	default:
		http.NotFound(w, r)
	}
}
func (d *DIAL) Discovery(packet []byte) string {
	if len(packet) > 1500 || !bytes.HasPrefix(packet, []byte("M-SEARCH * HTTP/1.1\r\n")) {
		return ""
	}
	r, e := http.ReadRequest(bufio.NewReader(bytes.NewReader(packet)))
	if e != nil {
		return ""
	}
	defer r.Body.Close()
	mx, e := strconv.Atoi(r.Header.Get("MX"))
	if e != nil || mx < 1 || mx > 5 || r.Header.Get("MAN") != `"ssdp:discover"` || r.Header.Get("ST") != dialService && r.Header.Get("ST") != "ssdp:all" {
		return ""
	}
	return "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: " + d.base + "/description.xml\r\nSERVER: Linux/1 UPnP/1.1 HomeSound/0.1\r\nST: " + dialService + "\r\nUSN: uuid:" + d.uuid + "::" + dialService + "\r\n\r\n"
}
func (d *DIAL) Notify(alive bool) string {
	state := "ssdp:byebye"
	if alive {
		state = "ssdp:alive"
	}
	return strings.Join([]string{"NOTIFY * HTTP/1.1", "HOST: 239.255.255.250:1900", "CACHE-CONTROL: max-age=1800", "LOCATION: " + d.base + "/description.xml", "NT: " + dialService, "NTS: " + state, "USN: uuid:" + d.uuid + "::" + dialService, "SERVER: Linux/1 UPnP/1.1 HomeSound/0.1", "", ""}, "\r\n")
}
