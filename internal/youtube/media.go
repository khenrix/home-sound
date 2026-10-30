package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"math"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const ResolverVersion = "2026.10.26"

var ErrUnsupported = errors.New("public unrestricted audio unavailable; restricted content is unsupported")
var ErrExpired = errors.New("media URL expired")

type Media struct {
	url      string
	headers  map[string]string
	Metadata airplay.Metadata
	Duration time.Duration
	Artwork  string
}

func (m Media) Expired(now time.Time) bool {
	u, e := url.Parse(m.url)
	if e != nil {
		return true
	}
	raw := u.Query().Get("expire")
	if raw == "" {
		return false
	}
	n, e := strconv.ParseInt(raw, 10, 64)
	return e != nil || now.Unix() >= n
}

type Resolver struct{ Helper, Runtime source.Helper }
type processReader struct {
	ctx context.Context
	p   *source.Process
}

func (r processReader) Read(b []byte) (int, error) { return r.p.Read(r.ctx, b) }
func helperResult(ctx context.Context, h source.Helper, args []string, limit int) ([]byte, error) {
	p, e := source.StartProcess(ctx, h, args, 30*time.Second)
	if e != nil {
		return nil, e
	}
	defer p.Close()
	b, e := io.ReadAll(io.LimitReader(processReader{ctx, p}, int64(limit+1)))
	if e != nil || len(b) > limit {
		return nil, errors.New("helper result failed or exceeded limit")
	}
	return b, nil
}
func display(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	if len(s) > 1024 {
		s = s[:1024]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
func (r Resolver) Resolve(ctx context.Context, id string) (Media, error) {
	if !videoPattern.MatchString(id) {
		return Media{}, errors.New("validated YouTube video ID required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	version, e := helperResult(ctx, r.Helper, []string{"--ignore-config", "--version"}, 128)
	if e != nil || strings.TrimSpace(string(version)) != ResolverVersion {
		return Media{}, errors.New("pinned yt-dlp version unavailable")
	}
	runtime, e := helperResult(ctx, r.Runtime, []string{"--version"}, 128)
	major := 0
	if e == nil {
		v := strings.TrimPrefix(strings.TrimSpace(string(runtime)), "v")
		major, _ = strconv.Atoi(strings.Split(v, ".")[0])
	}
	if e != nil || major < 20 {
		return Media{}, errors.New("pinned supported Node runtime unavailable")
	}
	args := []string{"--ignore-config", "--no-cookies", "--no-cookies-from-browser", "--no-playlist", "--no-cache-dir", "--no-plugin-dirs", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "node:" + r.Runtime.Path, "--xff", "never", "--socket-timeout", "10", "--retries", "1", "--extractor-retries", "1", "--skip-download", "--dump-single-json", "-f", "bestaudio[acodec=opus]/bestaudio[acodec=mp4a.40.2]", "--", "https://www.youtube.com/watch?v=" + id}
	b, e := helperResult(ctx, r.Helper, args, 1<<20)
	if e != nil {
		return Media{}, ErrUnsupported
	}
	var m struct {
		ID, Title, Uploader, Availability, URL, Thumbnail, ACodec, VCodec string
		Duration                                                          float64
		AgeLimit                                                          int               `json:"age_limit"`
		Live                                                              bool              `json:"is_live"`
		DRM                                                               bool              `json:"has_drm"`
		Headers                                                           map[string]string `json:"http_headers"`
	}
	if json.Unmarshal(b, &m) != nil || m.ID != id || m.Availability != "public" && m.Availability != "unlisted" || m.AgeLimit > 0 || m.Live || m.DRM || m.VCodec != "none" || m.ACodec != "opus" && m.ACodec != "mp4a.40.2" || math.IsNaN(m.Duration) || math.IsInf(m.Duration, 0) || m.Duration <= 0 || m.Duration > 21600 {
		return Media{}, ErrUnsupported
	}
	u, e := url.Parse(m.URL)
	if e != nil || u.Scheme != "https" || u.User != nil || !strings.HasSuffix(u.Hostname(), ".googlevideo.com") || len(m.URL) > 16384 {
		return Media{}, ErrUnsupported
	}
	headers := map[string]string{}
	if len(m.Headers) > 16 {
		return Media{}, ErrUnsupported
	}
	for k, v := range m.Headers {
		if strings.ContainsAny(k+v, "\r\n\x00") || len(v) > 2048 {
			return Media{}, ErrUnsupported
		}
		switch strings.ToLower(k) {
		case "user-agent", "accept", "accept-language", "origin", "referer":
			headers[k] = v
		case "cookie", "authorization", "proxy-authorization":
			return Media{}, ErrUnsupported
		}
	}

	metadata := airplay.Metadata{Title: display(m.Title), Artist: display(m.Uploader)}
	if a, e := url.Parse(m.Thumbnail); e == nil && a.User == nil {
		a.RawQuery = ""
		a.Fragment = ""
		candidate := metadata
		candidate.ArtworkID = "youtube:video:" + id
		candidate.ArtworkURL = a.String()
		if candidate.Validate() == nil {
			metadata = candidate
		}
	}
	return Media{url: m.URL, headers: headers, Metadata: metadata, Duration: time.Duration(m.Duration * float64(time.Second)), Artwork: metadata.ArtworkURL}, nil
}

type Decoder struct {
	Helper  source.Helper
	Version string
}

func (d Decoder) Decode(ctx context.Context, m Media, position time.Duration) (session.Source, error) {
	if m.Expired(time.Now()) {
		return nil, ErrExpired
	}
	if position < 0 || position >= m.Duration || m.Duration > 6*time.Hour || m.Duration <= 0 {
		return nil, errors.New("invalid bounded decoder position")
	}
	u, e := url.Parse(m.url)
	if e != nil || u.User != nil {
		return nil, ErrUnsupported
	}
	ip := net.ParseIP(u.Hostname())
	fixture := u.Scheme == "http" && ip != nil && ip.IsLoopback()
	if !fixture && (u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".googlevideo.com")) {
		return nil, ErrUnsupported
	}
	version, e := helperResult(ctx, d.Helper, []string{"-version"}, 8192)
	if e != nil || d.Version == "" || !strings.HasPrefix(string(version), "ffmpeg version "+d.Version+" ") {
		return nil, errors.New("pinned FFmpeg version unavailable")
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1", "-filter_threads", "1", "-rw_timeout", "10000000", "-protocol_whitelist", "https,tls,tcp,http,pipe", "-format_whitelist", "matroska,webm,mov,wav", "-probesize", "32768", "-analyzeduration", "1000000", "-reconnect", "0", "-reconnect_max_retries", "0"}
	keys := make([]string, 0, len(m.headers))
	for k := range m.headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var headers strings.Builder
	for _, k := range keys {
		headers.WriteString(k + ": " + m.headers[k] + "\r\n")
	}
	if headers.Len() > 0 {
		args = append(args, "-headers", headers.String())
	}
	if position > 0 {
		args = append(args, "-ss", strconv.FormatFloat(position.Seconds(), 'f', 6, 64))
	}
	args = append(args, "-i", m.url, "-map", "0:a:0", "-vn", "-sn", "-dn", "-t", strconv.FormatFloat((m.Duration-position).Seconds(), 'f', 6, 64), "-ac", "2", "-ar", "48000", "-c:a", "pcm_f32le", "-f", "f32le", "-flush_packets", "1", "-blocksize", "4096", "pipe:1")
	p, e := source.StartProcess(ctx, d.Helper, args, m.Duration-position+30*time.Second)
	if e != nil {
		return nil, e
	}
	pcm, e := source.NewPCM(p, 48000, m.Metadata)
	if e != nil {
		p.Close()
		return nil, e
	}
	return pcm, nil
}
