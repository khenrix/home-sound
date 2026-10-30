package spotify

import (
	"context"
	"encoding/json"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/source"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRESTVolumeUsesExternalStepsAndRejectsErrors(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/player/volume" || r.Method != "POST" {
			t.Error("wrong REST operation")
		}
		var v struct {
			Volume   int
			Relative bool
		}
		json.NewDecoder(r.Body).Decode(&v)
		if v.Volume != 23 || v.Relative {
			t.Error("wrong external volume")
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	a := &API{Origin: s.URL}
	if e := a.Volume(context.Background(), 23); e != nil {
		t.Fatal(e)
	}
	if e := a.Volume(context.Background(), 101); e == nil || calls != 1 {
		t.Fatal("invalid volume sent")
	}
}

func TestReconcileReadsActualStateButCannotAdmitRevokedLease(t *testing.T) {
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admissions, token := testAdmission(t, co)
	leases := make(chan source.Lease, 1)
	go (&Input{Group: config.AllSpeakers, Coordinator: co, Admissions: admissions, Lease: func(l source.Lease) { leases <- l }}).Run(ctx, a)
	go wire(b, 1, 0, nil, "playing", token)
	var old source.Lease
	select {
	case old = <-leases:
	case <-time.After(time.Second):
		t.Fatal("no source")
	}
	calls := 0
	paused := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/status" {
			t.Error(r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]bool{"stopped": false, "paused": paused, "active": true})
	}))
	defer server.Close()
	api := &API{Origin: server.URL}
	observed, current := api.Reconcile(ctx, co, old)
	if !current || !observed.Paused || o.Snapshot().Transport != "playing" || calls != 1 {
		t.Fatal("actual state not reconciled")
	}
	fresh, e := co.Reserve(source.Identity{Provider: "youtube", Group: config.TVAndSpeakers, Instance: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	paused = false
	_, current = api.Reconcile(ctx, co, old)
	if current || !co.Current(fresh) || calls != 2 {
		t.Fatal("queued active observation re-admitted old source")
	}
}
