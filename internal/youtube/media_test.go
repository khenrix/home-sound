package youtube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/source"
	"github.com/khenrix/home-sound/internal/wave"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	r := fixtureResolver(t, `{"id":"abcdefghijk","availability":"public","title":"Title","thumbnail":"https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg?public=1","duration":60,"acodec":"opus","vcodec":"none","url":"https://rr1.googlevideo.com/videoplayback?expire=9999999999","http_headers":{"User-Agent":"fixture"}}`)
	m, e := r.Resolve(context.Background(), "abcdefghijk")
	if e != nil {
		t.Fatalf("resolve public fixture: %v", e)
	}
	if m.Metadata.ArtworkID != "youtube:video:abcdefghijk" || m.Metadata.ArtworkURL != "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg" || m.Metadata.Title != "Title" || m.Duration != time.Minute || m.Expired(time.Now()) {
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
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav.Bytes())
	}))
	defer srv.Close()
	path, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Fatal("actual decoding fixture requires ffmpeg on PATH: install a supported FFmpeg or use the pinned CI fixture build")
	}
	path, e = filepath.Abs(path)
	if e != nil {
		t.Fatal(e)
	}
	// Pin the available codec fixture for this run; deployment pins remain separate.
	helper := pinnedFile(t, path)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, e := helperResult(ctx, helper, []string{"-version"}, 8192)
	if e != nil {
		t.Fatal(e)
	}
	fields := strings.Fields(strings.SplitN(string(output), "\n", 2)[0])
	if len(fields) < 3 || fields[0] != "ffmpeg" || fields[1] != "version" {
		t.Fatal("unrecognized FFmpeg version output")
	}
	d := Decoder{Helper: helper, Version: fields[2]}
	t.Logf("actual codec fixture: path=%s version=%s sha256=%s", helper.Path, d.Version, helper.SHA256)
	media := Media{url: srv.URL, Duration: time.Second, Metadata: airplay.Metadata{ArtworkID: "youtube:video:abcdefghijk", ArtworkURL: "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"}}
	t.Run("wrong_version_refused", func(t *testing.T) {
		wrong := d
		wrong.Version += "-unexpected"
		p, e := wrong.Decode(ctx, media, 0)
		if p != nil {
			p.Close()
		}
		if e == nil || e.Error() != "pinned FFmpeg version unavailable" {
			t.Fatalf("wrong version was not refused: %v", e)
		}
		if requests.Load() != 0 {
			t.Fatal("wrong-version decoder fetched media")
		}
	})
	p, e := d.Decode(ctx, media, 0)
	if e != nil {
		t.Fatalf("decoder: %v", e)
	}
	defer p.Close()
	if p.Metadata().ArtworkID != "youtube:video:abcdefghijk" {
		t.Fatal("decoder dropped artwork identity")
	}
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
