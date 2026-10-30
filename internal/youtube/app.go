package youtube

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

type NativeApp struct {
	mu      sync.Mutex
	Parent  context.Context
	Factory func() (*Lounge, *PlaybackController, error)
	cancel  context.CancelFunc
	done    chan struct{}
	lounge  *Lounge
}

func (a *NativeApp) Running() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.cancel != nil }
func (a *NativeApp) Launch(ctx context.Context, code string) error {
	a.mu.Lock()
	if a.cancel != nil {
		l := a.lounge
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.Ready():
			return l.Pair(ctx, code)
		}
	}
	l, p, e := a.Factory()
	if e != nil {
		a.mu.Unlock()
		return e
	}
	run, cancel := context.WithCancel(a.Parent)
	a.cancel = cancel
	a.lounge = l
	done := make(chan struct{})
	a.done = done
	a.mu.Unlock()
	go a.run(run, l, p, code, done)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-l.Ready():
		return nil
	case <-done:
		return errors.New("YouTube launch failed")
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	case <-timer.C:
		cancel()
		return errors.New("YouTube launch timed out")
	}
}
func (a *NativeApp) Stop() error {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			return errors.New("YouTube shutdown timed out")
		}
	}
	return nil
}
func (a *NativeApp) run(ctx context.Context, l *Lounge, p *PlaybackController, code string, done chan struct{}) {
	defer close(done)
	defer func() {
		a.mu.Lock()
		if a.done == done {
			a.cancel = nil
		}
		a.mu.Unlock()
	}()
	defer p.Close()
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	messages := make(chan Message, 16)
	reports := make(chan struct{}, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		defer cancel()
		_ = l.Run(run, code, func(m Message) error {
			select {
			case messages <- m:
				return nil
			default:
				return errors.New("Lounge control overflow")
			}
		})
	})
	workers.Go(func() {
		for {
			select {
			case <-run.Done():
				return
			case <-reports:
				select {
				case <-run.Done():
					return
				case <-l.Ready():
				}
				report(run, l, p)
			}
		}
	})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-run.Done():
			workers.Wait()
			return
		case m := <-messages:
			if p.Handle(run, m) != nil {
				continue
			}
		case <-ticker.C:
			p.Tick(run)
		}
		select {
		case reports <- struct{}{}:
		default:
		}
	}
}
func report(ctx context.Context, l *Lounge, p *PlaybackController) {
	s, _ := p.Snapshot()
	fields := map[string]string{"videoId": s.VideoID, "videoIds": strings.Join(s.IDs, ","), "currentIndex": strconv.Itoa(s.Index), "currentTime": strconv.FormatFloat(s.Position.Seconds(), 'f', 3, 64), "duration": strconv.FormatFloat(s.Duration.Seconds(), 'f', 3, 64), "seekableStartTime": "0", "seekableEndTime": strconv.FormatFloat(s.Duration.Seconds(), 'f', 3, 64), "state": strconv.Itoa(s.State)}
	for _, name := range []string{"nowPlaying", "onStateChange", "onVolumeChanged"} {
		send, cancel := context.WithTimeout(ctx, 2*time.Second)
		v := fields
		if name == "onVolumeChanged" {
			v = map[string]string{"volume": strconv.Itoa(s.Volume), "muted": strconv.FormatBool(s.Muted)}
		}
		e := l.Send(send, name, v)
		cancel()
		if e != nil {
			return
		}
	}
}
