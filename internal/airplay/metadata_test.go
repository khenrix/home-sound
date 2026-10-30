package airplay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtworkIdentityRoundTripAndValidation(t *testing.T) {
	raw := []byte(`{"Title":"track","ArtworkID":"spotify:image:0123456789abcdef0123456789abcdef01234567","ArtworkURL":"https://i.scdn.co/image/0123456789abcdef0123456789abcdef01234567"}`)
	var m Metadata
	if e := json.Unmarshal(raw, &m); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(m)
	var fields map[string]any
	json.Unmarshal(b, &fields)
	if fields["ArtworkURL"] != "https://i.scdn.co/image/0123456789abcdef0123456789abcdef01234567" {
		t.Fatal("artwork discarded from shared metadata")
	}
	v, ok := any(m).(interface{ Validate() error })
	if !ok || v.Validate() != nil {
		t.Fatal("bounded artwork validator missing")
	}
}

func TestArtworkRejectsMismatchesSecretsAndUnboundedValues(t *testing.T) {
	valid := Metadata{ArtworkID: "youtube:video:abcdefghijk", ArtworkURL: "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"}
	if e := valid.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, u := range []string{"http://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg", "https://user:secret@i.ytimg.com/vi/abcdefghijk/hqdefault.jpg", "https://i.ytimg.com/vi/otherid1234/hqdefault.jpg", "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg?token=private", "https://127.0.0.1/a", "https://i.ytimg.com:443/vi/abcdefghijk/hqdefault.jpg", strings.Repeat("x", 2049)} {
		m := valid
		m.ArtworkURL = u
		if m.Validate() == nil {
			t.Fatalf("accepted invalid artwork URL %q", u)
		}
	}
	valid.ArtworkID = ""
	if valid.Validate() == nil {
		t.Fatal("URL without artwork identity")
	}
	if (Metadata{}).Validate() != nil {
		t.Fatal("explicit artwork clear rejected")
	}
}
