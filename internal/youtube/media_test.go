package youtube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/source"
	"github.com/khenrix/home-sound/internal/wave"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pinnedFile(t *testing.T, path string) source.Helper {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(b)
	return source.Helper{Path: path, SHA256: hex.EncodeToString(h[:])}
}
func scriptHelper(t *testing.T, body string) source.Helper {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper.py")
	if e := os.WriteFile(p, []byte("#!/usr/bin/python3\n"+body), 0700); e != nil {
		t.Fatal(e)
	}
	return pinnedFile(t, p)
}
func fixtureResolver(t *testing.T, payload string) Resolver {
	runtime := scriptHelper(t, "print('v26.2.0')\n")
	helper := scriptHelper(t, `import sys,json
if '--version' in sys.argv: print('2026.10.26'); sys.exit(0)
required=['--ignore-config','--no-cookies','--no-cookies-from-browser','--no-cache-dir','--no-playlist','--no-remote-components','--no-plugin-dirs','--no-js-runtimes','--xff','never','--dump-single-json']
if not all(x in sys.argv for x in required): sys.exit(9)
if sys.argv[-1] != 'https://www.youtube.com/watch?v=abcdefghijk': sys.exit(8)
print(`+strings.TrimSpace(string(mustJSON(t, payload)))+`)
`)
	return Resolver{Helper: helper, Runtime: runtime}
}
func mustJSON(t *testing.T, s string) []byte {
	t.Helper()
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestResolverUsesPinnedRestrictedArgvAndPrivateMedia(t *testing.T) {
	r := fixtureResolver(t, `{"id":"abcdefghijk","availability":"public","title":"Title","duration":60,"acodec":"opus","vcodec":"none","url":"https://rr1.googlevideo.com/videoplayback?expire=9999999999","http_headers":{"User-Agent":"fixture"}}`)
	m, e := r.Resolve(context.Background(), "abcdefghijk")
	if e != nil {
		t.Fatalf("resolve public fixture: %v", e)
	}
	if m.Metadata.Title != "Title" || m.Duration != time.Minute || m.Expired(time.Now()) {
		t.Fatalf("metadata %+v", m.Metadata)
	}
	if _, e = r.Resolve(context.Background(), "../../cookie"); e == nil {
		t.Fatal("accepted arbitrary resolver input")
	}
}
func TestResolverRejectsRestrictedAndExpiredContent(t *testing.T) {
	for _, payload := range []string{`{"id":"abcdefghijk","availability":"private"}`, `{"id":"abcdefghijk","availability":"public","age_limit":18}`, `{"id":"abcdefghijk","availability":"public","has_drm":true}`} {
		r := fixtureResolver(t, payload)
		if _, e := r.Resolve(context.Background(), "abcdefghijk"); e == nil {
			t.Fatal("restricted content accepted")
		}
	}
	m := Media{url: "https://rr1.googlevideo.com/videoplayback?expire=1"}
	if !m.Expired(time.Now()) {
		t.Fatal("expired URL accepted")
	}
}
func TestActualFFmpegDecodesBoundedPublicTransportFixture(t *testing.T) {
	var wav bytes.Buffer
	samples := make([]float32, 960)
	for i := range samples {
		samples[i] = .25
	}
	if e := wave.Write(&wav, audio.Canonical, samples); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav.Bytes())
	}))
	defer srv.Close()
	d := Decoder{Helper: pinnedFile(t, "/usr/bin/ffmpeg"), Version: "8.0.1-3ubuntu2"}
	p, e := d.Decode(context.Background(), Media{url: srv.URL, Duration: time.Second}, 0)
	if e != nil {
		t.Fatalf("decoder: %v", e)
	}
	defer p.Close()
	var frames int
	for {
		f, e := p.Read(context.Background())
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		frames += len(f.Data) / 2
		for _, v := range f.Data {
			if math.Abs(float64(v-.25)) > .0001 {
				t.Fatalf("decoded sample %f", v)
			}
		}
	}
	if frames != 480 {
		t.Fatalf("decoded %d frames", frames)
	}
}
