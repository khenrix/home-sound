package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/source"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestAdmissionFencesQueuedCommandAndRevokedGrant(t *testing.T) {
	ctx := context.Background()
	clock, e := session.NewRawClock()
	if e != nil {
		t.Fatal(e)
	}
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	b := NewAdmissions(ctx, co, "All Speakers", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	oldMarker := int64(clock.Now())
	token, e := b.Request(AdmissionRequest{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1, oldMarker, ""})
	if e != nil || token == "" {
		t.Fatalf("fresh native command not admitted: %v", e)
	}
	_, e = co.Reserve(source.Identity{Provider: "youtube", Group: "TV + All Speakers", Instance: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Request(AdmissionRequest{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2, oldMarker, ""}); e == nil {
		t.Fatal("queued command predating handoff reacquired ownership")
	}
	fresh, e := b.Request(AdmissionRequest{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 3, int64(clock.Now()), ""})
	if e != nil || fresh == token {
		t.Fatalf("fresh explicit recast failed: %v", e)
	}
	if _, e = b.Request(AdmissionRequest{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 3, int64(clock.Now()), ""}); e == nil {
		t.Fatal("native command sequence replayed")
	}
}

const testInstance = "cccccccccccccccccccccccccccccccc"

func testAdmission(t *testing.T, co *source.Coordinator) (*Admissions, string) {
	t.Helper()
	b := NewAdmissions(t.Context(), co, config.AllSpeakers, testInstance)
	clock, _ := session.NewRawClock()
	token, e := b.Request(AdmissionRequest{Instance: testInstance, Sequence: 1, Received: int64(clock.Now())})
	if e != nil {
		t.Fatal(e)
	}
	return b, token
}

func testAdmissionServer(t *testing.T, ctx context.Context, co *source.Coordinator) (*Admissions, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "admit.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	b := NewAdmissions(ctx, co, config.AllSpeakers, testInstance)
	go b.Serve(ctx, l)
	return b, socket
}

func TestAdmissionIPCDelayedBeforeHandoffAndFreshReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	b, path := testAdmissionServer(t, ctx, co)
	clock, _ := session.NewRawClock()
	oldMarker := int64(clock.Now())
	conn, e := net.Dial("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	// The request has begun but cannot be decoded before another owner wins.
	fmt.Fprintf(conn, "{\"Instance\":%q,\"Sequence\":1,\"Received\":%d", testInstance, oldMarker)
	fresh, e := co.Reserve(source.Identity{Provider: "youtube", Group: config.TVAndSpeakers, Instance: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprint(conn, "}\n")
	var reply struct {
		Grant    string
		Retained bool
	}
	if e = json.NewDecoder(conn).Decode(&reply); e != nil {
		t.Fatal(e)
	}
	if reply.Grant != "" || !co.Current(fresh) {
		t.Fatal("delayed native grant stole ownership")
	}
	token, e := b.Request(AdmissionRequest{Instance: testInstance, Sequence: 2, Received: int64(clock.Now())})
	if e != nil {
		t.Fatal(e)
	}
	g, l, e := b.bind(token, testInstance, "11111111111111111111111111111111", 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = b.bind(token, testInstance, "22222222222222222222222222222222", 1); e == nil {
		t.Fatal("old grant allowed reconnect")
	}
	retained, e := b.Request(AdmissionRequest{Instance: testInstance, Sequence: 3, Received: int64(clock.Now()), Retain: token})
	if e != nil || retained != token || !co.Current(l) {
		t.Fatal("ordinary current transfer restarted media")
	}
	co.End(l)
	if g.ctx.Err() == nil {
		t.Fatal("grant not canceled")
	}
	if _, _, e = b.bind(token, testInstance, "22222222222222222222222222222222", 1); e == nil {
		t.Fatal("revoked grant bound")
	}
	replacement, e := b.Request(AdmissionRequest{Instance: testInstance, Sequence: 4, Received: int64(clock.Now()), Retain: token})
	if e != nil || replacement == token {
		t.Fatal("fresh recast did not replace revoked grant")
	}
	if _, _, e = b.bind(replacement, testInstance, "22222222222222222222222222222222", 1); e != nil {
		t.Fatal(e)
	}
}

type admissionSeekSink struct {
	*source.Observer
	selections       int
	entered, release chan struct{}
}

func (s *admissionSeekSink) Select(v session.Selection) (uint64, error) {
	s.selections++
	if s.selections == 2 {
		close(s.entered)
		<-s.release
	}
	return s.Observer.Select(v)
}
func TestRetainingNativeTransferDuringSeekKeepsGrant(t *testing.T) {
	sink := &admissionSeekSink{Observer: source.NewObserver(), entered: make(chan struct{}), release: make(chan struct{})}
	defer sink.Stop()
	co := source.NewCoordinator(sink, nil)
	admissions, token := testAdmission(t, co)
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leases := make(chan source.Lease, 3)
	go (&Input{Group: config.AllSpeakers, Coordinator: co, Admissions: admissions, Lease: func(l source.Lease) { leases <- l }}).Run(ctx, a)
	go func() { wire(b, 1, 0, nil, "paused", token); wire(b, 2, 0, nil, "paused", token) }()
	select {
	case <-leases:
	case <-time.After(time.Second):
		t.Fatal("no initial source")
	}
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("seek not entered")
	}
	clock, _ := session.NewRawClock()
	reply := make(chan string, 1)
	go func() {
		v, _ := admissions.Request(AdmissionRequest{Instance: testInstance, Sequence: 2, Received: int64(clock.Now()), Retain: token})
		reply <- v
	}()
	// Give the retaining request a chance to contend while Select is blocked.
	time.Sleep(20 * time.Millisecond)
	close(sink.release)
	select {
	case got := <-reply:
		if got != token {
			t.Fatal("current retaining transfer minted new grant while seek was in flight")
		}
	case <-time.After(time.Second):
		t.Fatal("admission/seek lock cycle")
	}
}
