package youtube

import (
	"testing"
	"time"
)

func TestYouTubeQueueTransportSeekAndVolume(t *testing.T) {
	q := NewQueue()
	apply := func(name string, v map[string]string) Action {
		t.Helper()
		a, e := q.Apply(Message{Name: name, Values: v})
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	a := apply("setPlaylist", map[string]string{"videoIds": "abcdefghijk,lmnopqrstuv", "currentIndex": "0", "currentTime": "1.25"})
	if a.Kind != "load" || a.VideoID != "abcdefghijk" || a.Position != 1250*time.Millisecond {
		t.Fatalf("playlist %+v", a)
	}
	a = apply("updatePlaylist", map[string]string{"videoIds": "zzzzzzzzzzz,abcdefghijk,lmnopqrstuv"})
	if a.Kind != "queue" {
		t.Fatalf("queue update restarted audio %+v", a)
	}
	apply("pause", nil)
	a = apply("seekTo", map[string]string{"newTime": "3.5"})
	if a.Kind != "load" || !a.Paused || a.Position != 3500*time.Millisecond {
		t.Fatalf("paused seek %+v", a)
	}
	a = apply("next", nil)
	if a.VideoID != "lmnopqrstuv" {
		t.Fatalf("next %+v", a)
	}
	a = apply("previous", nil)
	if a.VideoID != "abcdefghijk" {
		t.Fatalf("previous %+v", a)
	}
	a = apply("setVolume", map[string]string{"volume": "37", "muted": "true"})
	if a.Volume != 37 || a.Muted == nil || !*a.Muted {
		t.Fatalf("volume %+v", a)
	}
	if a = apply("getNowPlaying", nil); a.Kind != "now" {
		t.Fatal(a)
	}
	if a = apply("stopVideo", nil); a.Kind != "stop" {
		t.Fatal(a)
	}
	if q.Snapshot().State != 4 {
		t.Fatal("wrong Lounge stopped state")
	}
}
func TestYouTubeQueueRejectsMalformedWithoutMutation(t *testing.T) {
	q := NewQueue()
	for _, m := range []Message{{Name: "setPlaylist", Values: map[string]string{"videoIds": "../../file"}}, {Name: "seekTo", Values: map[string]string{"newTime": "NaN"}}, {Name: "setVolume", Values: map[string]string{"volume": "101"}}, {Name: "setPlaylist", Values: map[string]string{"videoIds": "abcdefghijk", "currentIndex": "-1"}}} {
		if _, e := q.Apply(m); e == nil {
			t.Fatalf("accepted %+v", m)
		}
	}
	if q.Snapshot().State != -1 {
		t.Fatal("invalid command changed state")
	}
}
