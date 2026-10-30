package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
