package airplay

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var spotifyArtworkID = regexp.MustCompile(`^spotify:image:([a-f0-9]{40})$`)
var youtubeArtworkID = regexp.MustCompile(`^youtube:video:([A-Za-z0-9_-]{11})$`)
var youtubeArtworkFile = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}\.(jpg|webp)$`)

// Validate keeps artwork an inert, bounded public identity. Fetching image bytes
// and native receiver artwork transmission are separate transport responsibilities.
func (m Metadata) Validate() error {
	for _, s := range []string{m.Title, m.Artist, m.Album} {
		if len(s) > 1024 || strings.ContainsAny(s, "\r\n\x00") {
			return errors.New("invalid display metadata")
		}
	}
	if m.ArtworkID == "" && m.ArtworkURL == "" {
		return nil
	}
	if len(m.ArtworkID) > 128 || len(m.ArtworkURL) > 2048 {
		return errors.New("artwork limit")
	}
	u, e := url.Parse(m.ArtworkURL)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("invalid artwork URL")
	}
	if id := spotifyArtworkID.FindStringSubmatch(m.ArtworkID); id != nil && u.Host == "i.scdn.co" && u.Path == "/image/"+id[1] {
		return nil
	}
	if id := youtubeArtworkID.FindStringSubmatch(m.ArtworkID); id != nil && u.Host == "i.ytimg.com" {
		parts := strings.Split(u.Path, "/")
		if len(parts) == 4 && (parts[1] == "vi" || parts[1] == "vi_webp") && parts[2] == id[1] && youtubeArtworkFile.MatchString(parts[3]) {
			return nil
		}
	}
	return errors.New("artwork identity/URL mismatch")
}
