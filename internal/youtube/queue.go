package youtube

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var videoPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type Action struct {
	Kind, VideoID string
	Position      time.Duration
	Paused        bool
	Volume        int
	Muted         *bool
}
type Playback struct {
	VideoID              string
	Position             time.Duration
	Duration             time.Duration
	State, Volume, Index int
	Muted                bool
	IDs                  []string
}
type Queue struct {
	mu         sync.Mutex
	p          Playback
	paused     bool
	generation uint64
}

func NewQueue() *Queue { return &Queue{p: Playback{State: -1}} }
func seconds(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	v, e := strconv.ParseFloat(s, 64)
	if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 21600 {
		return 0, errors.New("invalid media position")
	}
	return time.Duration(v * float64(time.Second)), nil
}
func videoIDs(s string) ([]string, error) {
	ids := strings.Split(s, ",")
	if len(ids) == 0 || len(ids) > 100 {
		return nil, errors.New("queue size limit")
	}
	for _, id := range ids {
		if !videoPattern.MatchString(id) {
			return nil, errors.New("invalid YouTube video ID")
		}
	}
	return ids, nil
}
func (q *Queue) load() Action {
	q.p.State = 3
	return Action{Kind: "load", VideoID: q.p.VideoID, Position: q.p.Position, Paused: q.paused}
}
func (q *Queue) Apply(m Message) (Action, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.applyLocked(m)
}
func (q *Queue) applyLocked(m Message) (Action, error) {
	v := m.Values
	switch m.Name {
	case "setPlaylist":
		ids, e := videoIDs(v["videoIds"])
		if e != nil {
			return Action{}, e
		}
		index := 0
		if v["currentIndex"] != "" {
			index, e = strconv.Atoi(v["currentIndex"])
		}
		position, pe := seconds(v["currentTime"])
		if e != nil || pe != nil || index < 0 || index >= len(ids) {
			return Action{}, errors.New("invalid playlist position")
		}
		same := q.p.VideoID == ids[index] && q.p.Position == position
		q.p.IDs = ids
		q.p.Index = index
		q.p.VideoID = ids[index]
		q.p.Position = position
		if same {
			return Action{Kind: "queue"}, nil
		}
		q.paused = false
		return q.load(), nil
	case "updatePlaylist":
		ids, e := videoIDs(v["videoIds"])
		if e != nil {
			return Action{}, e
		}
		for i, id := range ids {
			if id == q.p.VideoID {
				q.p.IDs = ids
				q.p.Index = i
				return Action{Kind: "queue"}, nil
			}
		}
		q.p.IDs = ids
		q.p.Index = 0
		q.p.VideoID = ids[0]
		q.p.Position = 0
		return q.load(), nil
	case "pause":
		if q.p.VideoID == "" {
			return Action{}, errors.New("no video")
		}
		q.paused = true
		q.p.State = 2
		return Action{Kind: "pause"}, nil
	case "play":
		if q.p.VideoID == "" {
			return Action{}, errors.New("no video")
		}
		q.paused = false
		if q.p.State == 4 {
			q.p.Position = 0
			return q.load(), nil
		}
		q.p.State = 1
		return Action{Kind: "resume"}, nil
	case "seekTo":
		position, e := seconds(v["newTime"])
		if e != nil || q.p.VideoID == "" {
			return Action{}, errors.New("invalid seek")
		}
		q.p.Position = position
		return q.load(), nil
	case "next", "previous":
		index := q.p.Index + 1
		if m.Name == "previous" {
			index = q.p.Index - 1
		}
		if index < 0 || index >= len(q.p.IDs) {
			q.p.State = 4
			return Action{Kind: "stop"}, nil
		}
		q.p.Index = index
		q.p.VideoID = q.p.IDs[index]
		q.p.Position = 0
		return q.load(), nil
	case "stopVideo":
		q.p.State = 4
		return Action{Kind: "stop"}, nil
	case "setVolume":
		volume, e := strconv.Atoi(v["volume"])
		if e != nil || volume < 0 || volume > 100 {
			return Action{}, errors.New("invalid group volume")
		}
		var muted *bool
		if raw, ok := v["muted"]; ok {
			if raw != "true" && raw != "false" {
				return Action{}, errors.New("invalid mute")
			}
			b := raw == "true"
			muted = &b
		}
		q.p.Volume = volume
		if muted != nil {
			q.p.Muted = *muted
		}
		return Action{Kind: "volume", Volume: volume, Muted: muted}, nil
	case "getVolume":
		return Action{Kind: "volume_status", Volume: q.p.Volume}, nil
	case "getNowPlaying":
		return Action{Kind: "now"}, nil
	default:
		return Action{Kind: "unsupported"}, nil
	}
}
func (q *Queue) Snapshot() Playback {
	q.mu.Lock()
	defer q.mu.Unlock()
	p := q.p
	p.IDs = append([]string(nil), p.IDs...)
	return p
}
func (q *Queue) Observed(position time.Duration, ended bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.p.Position = position
	if ended {
		q.p.State = 4
	} else if q.paused {
		q.p.State = 2
	} else {
		q.p.State = 1
	}
}

func (q *Queue) Paused() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.paused }

func (q *Queue) setDuration(d time.Duration) { q.mu.Lock(); defer q.mu.Unlock(); q.p.Duration = d }

func (q *Queue) ApplyGeneration(m Message, g uint64) (Action, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	a, e := q.applyLocked(m)
	if e == nil && a.Kind == "load" {
		q.generation = g
	}
	return a, e
}
func (q *Queue) ObservedGeneration(g uint64, position time.Duration, ended bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.generation != g {
		return false
	}
	q.p.Position = position
	if ended {
		q.p.State = 4
	} else if q.paused {
		q.p.State = 2
	} else {
		q.p.State = 1
	}
	return true
}

func (q *Queue) initialVolume(v int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if v >= 0 && v <= 100 {
		q.p.Volume = v
	}
}
