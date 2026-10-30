package inputs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the production argv against the actual pinned CLI parser. Deliberate
// invalid configuration makes loadCLIConfig exit before constructing any API,
// advertisement, authentication, device or audio runtime.
func TestActualSpotifyCLIReadsSelectedConfigDirectory(t *testing.T) {
	helper := os.Getenv("HOME_SOUND_SPOTIFY_HELPER")
	if helper == "" {
		t.Skip("explicit separately built pinned Spotify executable required")
	}
	dir := filepath.Join(t.TempDir(), "selected config")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("cache:\n  size_limit: home_sound_parser_stop\n"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, spotifyArgs(dir)...)
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir())
	output, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("CLI did not stop at config parsing")
	}
	if e == nil || !strings.Contains(string(output), "invalid cache.size_limit") || !strings.Contains(string(output), "home_sound_parser_stop") {
		t.Fatalf("actual helper never read the selected config (exit %v): %s", e, output)
	}
}
