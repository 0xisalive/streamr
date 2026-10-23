package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadStripsQuotes guards the bug where quoted values in the config file
// (as written in config.example.yaml) were parsed WITH their surrounding
// quotes, producing invalid paths/addresses — e.g. web_dir `"web/dist"` with
// literal quotes, which does not exist, causing GET / to 404.
func TestLoadStripsQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
addr: ":8080"
web_dir: "web/dist"
data_dir: './data'
tmdb_api_key: "abc123"
engine: embedded
torznab: name=prowlarr url=http://localhost:9696/1/api key=KEY enabled=true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := map[string]string{
		"addr":         cfg.Addr,
		"web_dir":      cfg.WebDir,
		"data_dir":     cfg.DataDir,
		"tmdb_api_key": cfg.TMDBAPIKey,
	}
	want := map[string]string{
		"addr":         ":8080",
		"web_dir":      "web/dist",
		"data_dir":     "./data",
		"tmdb_api_key": "abc123",
	}
	for k, got := range cases {
		if got != want[k] {
			t.Errorf("%s = %q, want %q (surrounding quotes not stripped)", k, got, want[k])
		}
	}
	if len(cfg.Torznab) != 1 || cfg.Torznab[0].Name != "prowlarr" || cfg.Torznab[0].URL != "http://localhost:9696/1/api" {
		t.Errorf("indexer parsed wrong: %+v", cfg.Torznab)
	}
}

func TestUnquote(t *testing.T) {
	cases := map[string]string{
		`"x"`:   "x",
		`'y'`:   "y",
		`z`:     "z",
		`""`:    "",
		`"a'b"`: "a'b",
		`"`:     `"`, // single char, not a pair
	}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateWarnsMissingWebDir ensures a missing/empty web_dir produces an
// explicit warning rather than a silent 404 at runtime.
func TestValidateWarnsMissingWebDir(t *testing.T) {
	c := Default()
	c.TMDBAPIKey = "x"
	c.WebDir = ""
	if !hasWarn(c.Validate(), "web_dir not set") {
		t.Error("expected a warning when web_dir is empty")
	}

	c.WebDir = "/definitely/does/not/exist"
	if !hasWarn(c.Validate(), "does not exist") {
		t.Error("expected a warning when web_dir path is missing")
	}
}

func hasWarn(warns []string, substr string) bool {
	for _, w := range warns {
		if len(w) >= len(substr) && contains(w, substr) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
