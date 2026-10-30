package youtube

import (
	"context"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"testing"
	"time"
)

type resolveFixture struct {
	calls chan string
	wait  chan struct{}
}

func (r *resolveFixture) Resolve(ctx context.Context, id string) (Media, error) {
	r.calls <- id
	select {
	case <-ctx.Done():
		return Media{}, ctx.Err()
	case <-r.wait:
		return Media{Metadata: airplay.Metadata{Title: id}, Duration: time.Minute}, nil
	}
}

type decodeFixture struct{}

func (decodeFixture) Decode(context.Context, Media, time.Duration) (session.Source, error) {
	return &playFixture{}, nil
}

type playFixture struct{ n uint64 }

func (*playFixture) Format() audio.Format       { return audio.Canonical }
func (*playFixture) Metadata() airplay.Metadata { return airplay.Metadata{Title: "decoded"} }
func (*playFixture) Close() error               { return nil }
func (s *playFixture) Read(context.Context) (audio.Frame, error) {
	if s.n > 48000 {
		return audio.Frame{}, io.EOF
	}
	f := audio.Frame{Sample: s.n, Data: make([]float32, 1920)}
	s.n += 960
	return f, nil
}
func TestPlaybackLateResolveAndPausedSeek(t *testing.T) {
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	r := &resolveFixture{make(chan string, 8), make(chan struct{})}
	p := NewPlaybackController("All Speakers", "22222222222222222222222222222222", co, r, decodeFixture{}, -30)
	defer p.Close()
	ctx := context.Background()
	if e := p.Handle(ctx, Message{Name: "setPlaylist", Values: map[string]string{"videoIds": "abcdefghijk"}}); e != nil {
		t.Fatal(e)
	}
	<-r.calls
	if e := p.Handle(ctx, Message{Name: "pause"}); e != nil {
		t.Fatal(e)
	}
	if e := p.Handle(ctx, Message{Name: "seekTo", Values: map[string]string{"newTime": "12"}}); e != nil {
		t.Fatal(e)
	}
	<-r.calls
	close(r.wait)
	deadline := time.Now().Add(time.Second)
	for o.Snapshot().Epoch == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s := o.Snapshot(); s.Epoch != 1 || s.Transport != "paused" || s.Frames != 0 {
		t.Fatalf("late resolver or paused seek admitted wrong source: %+v", s)
	}
	p.Handle(ctx, Message{Name: "play"})
	time.Sleep(30 * time.Millisecond)
	if o.Snapshot().Frames == 0 {
		t.Fatal("prepared paused source cannot resume")
	}
}

type eofDecoder struct{ calls chan string }

func (d eofDecoder) Decode(ctx context.Context, m Media, _ time.Duration) (session.Source, error) {
	d.calls <- m.Metadata.Title
	return &oneFrame{}, nil
}

type oneFrame struct {
	playFixture
	read bool
}

func (s *oneFrame) Read(context.Context) (audio.Frame, error) {
	if s.read {
		return audio.Frame{}, io.EOF
	}
	s.read = true
	return audio.Frame{Data: make([]float32, 1920)}, nil
}
func TestPlaybackEOFAdvancesOnlyExplicitQueue(t *testing.T) {
	o := source.NewObserver()
	defer o.Stop()
	r := &resolveFixture{make(chan string, 8), make(chan struct{})}
	close(r.wait)
	calls := make(chan string, 8)
	p := NewPlaybackController("All Speakers", "33333333333333333333333333333333", source.NewCoordinator(o, nil), r, eofDecoder{calls}, -30)
	defer p.Close()
	tick, ok := any(p).(interface{ Tick(context.Context) })
	if !ok {
		t.Fatal("completed source has no explicit queue advance")
	}
	ctx := context.Background()
	if e := p.Handle(ctx, Message{Name: "setPlaylist", Values: map[string]string{"videoIds": "abcdefghijk,lmnopqrstuv"}}); e != nil {
		t.Fatal(e)
	}
	if <-calls != "abcdefghijk" {
		t.Fatal("wrong first video")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tick.Tick(ctx)
		select {
		case id := <-calls:
			if id != "lmnopqrstuv" {
				t.Fatal(id)
			}
			return
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatal("actual EOF did not advance queue")
}

type retryDecoder struct{ calls int }

func (d *retryDecoder) Decode(context.Context, Media, time.Duration) (session.Source, error) {
	d.calls++
	return &failedFrame{}, nil
}

type failedFrame struct{ playFixture }

func (*failedFrame) Read(context.Context) (audio.Frame, error) {
	return audio.Frame{}, io.ErrUnexpectedEOF
}
func TestPlaybackDecodeFailureReResolvesOnlyOnce(t *testing.T) {
	o := source.NewObserver()
	defer o.Stop()
	r := &resolveFixture{make(chan string, 8), make(chan struct{})}
	close(r.wait)
	d := &retryDecoder{}
	p := NewPlaybackController("All Speakers", "44444444444444444444444444444444", source.NewCoordinator(o, nil), r, d, -30)
	defer p.Close()
	p.Handle(context.Background(), Message{Name: "setPlaylist", Values: map[string]string{"videoIds": "abcdefghijk"}})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, e := p.Snapshot()
		if e != "" {
			if d.calls != 2 || len(r.calls) != 2 || o.Snapshot().Epoch != 0 {
				t.Fatalf("unbounded retry or failed source admitted: %d %d", d.calls, len(r.calls))
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("decoder failure never reached terminal state")
}

type lateFailureDecoder struct{}

func (lateFailureDecoder) Decode(context.Context, Media, time.Duration) (session.Source, error) {
	return &lateFailure{}, nil
}

type lateFailure struct {
	playFixture
	read bool
}

func (s *lateFailure) Read(context.Context) (audio.Frame, error) {
	if s.read {
		return audio.Frame{}, io.ErrUnexpectedEOF
	}
	s.read = true
	return audio.Frame{Data: make([]float32, 1920)}, nil
}
func TestPlaybackLateDecoderFailureIsTerminalNotPlaying(t *testing.T) {
	o := source.NewObserver()
	defer o.Stop()
	r := &resolveFixture{make(chan string, 8), make(chan struct{})}
	close(r.wait)
	p := NewPlaybackController("All Speakers", "55555555555555555555555555555555", source.NewCoordinator(o, nil), r, lateFailureDecoder{}, -30)
	defer p.Close()
	p.Handle(context.Background(), Message{Name: "setPlaylist", Values: map[string]string{"videoIds": "abcdefghijk,lmnopqrstuv"}})
	deadline := time.Now().Add(time.Second)
	for o.Snapshot().Transport != "error" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s, e := p.Snapshot()
	if s.State != 4 || e == "" {
		t.Fatalf("failed decoder still reported playing: %+v %q", s, e)
	}
	p.Tick(context.Background())
	if len(r.calls) != 1 {
		t.Fatal("decoder failure skipped to a different queue item")
	}
}
