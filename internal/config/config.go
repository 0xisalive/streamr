// Package config loads streamr's runtime configuration from a YAML-ish file
// and environment variables. To keep the module dependency-free it uses a tiny
// built-in parser for a flat/one-level-nested key: value format (documented in
// config.example.yaml). Secrets can also come from the environment.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// HTTP
	Addr    string // listen address, e.g. ":8080"
	WebDir  string // path to built frontend (web/dist); empty = API only
	DataDir string // working dir for downloads, cache, sqlite

	// Discovery
	TMDBAPIKey string

	// Torrent engine selection: "embedded" (default; no external daemon, ships
	// in the binary) or "qbittorrent" (drive an existing qBittorrent WebUI).
	Engine string

	// qBittorrent WebUI (only used when Engine == "qbittorrent")
	QbitURL     string
	QbitUser    string
	QbitPass    string
	QbitSaveDir string // where qBittorrent writes files (must be readable by streamr)

	// Sources — maximize coverage by listing many indexers.
	Torznab []IndexerConfig // torrent indexers (Prowlarr/Jackett/raw Torznab)
	Newznab []IndexerConfig // usenet indexers

	// Subtitle pipeline
	SubMinConfidence    float64
	WhisperXEnabled     bool   // opt-in Tier-3 aligner (forced alignment)
	WhisperGenEnabled   bool   // DEFAULT: generate subtitles from audio (quota-free)
	WhisperModel        string // tiny|base|small|medium|large (default "base")
	WhisperDevice       string // "cpu" | "cuda" (default "cpu")
	OpenSubtitlesAPIKey string
	OpenSubtitlesUA     string

	// Streaming session management
	SessionIdleTimeoutMinutes int
}

type IndexerConfig struct {
	Name    string
	URL     string
	APIKey  string
	Enabled bool
}

// Default returns config with sensible defaults before file/env overrides.
func Default() Config {
	return Config{
		Addr:                      ":8080",
		DataDir:                   "./data",
		Engine:                    "embedded", // self-contained; no external client to install
		SubMinConfidence:          0.55,
		WhisperGenEnabled:         true, // quota-free subtitles by default
		WhisperModel:              "base",
		WhisperDevice:             "cpu",
		SessionIdleTimeoutMinutes: 60,
	}
}

// Load reads defaults, then the file at path (if it exists), then environment
// overrides. Env wins so secrets can stay out of the file.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if err := cfg.loadFile(path); err != nil && !os.IsNotExist(err) {
			return cfg, err
		}
	}
	cfg.loadEnv()
	return cfg, nil
}

// loadFile parses a minimal "key: value" format. Indexers are expressed as
// repeated "torznab:" / "newznab:" lines of the form:
//
//	torznab: name=prowlarr url=http://localhost:9696/1/api key=abc enabled=true
func (c *Config) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		val = unquote(strings.TrimSpace(val))

		switch key {
		case "addr":
			c.Addr = val
		case "web_dir":
			c.WebDir = val
		case "data_dir":
			c.DataDir = val
		case "tmdb_api_key":
			c.TMDBAPIKey = val
		case "qbit_url":
			c.QbitURL = val
		case "qbit_user":
			c.QbitUser = val
		case "qbit_pass":
			c.QbitPass = val
		case "qbit_save_dir":
			c.QbitSaveDir = val
		case "engine":
			c.Engine = val
		case "sub_min_confidence":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				c.SubMinConfidence = v
			}
		case "whisperx_enabled":
			c.WhisperXEnabled = parseBool(val)
		case "whisper_gen_enabled":
			c.WhisperGenEnabled = parseBool(val)
		case "whisper_model":
			c.WhisperModel = val
		case "whisper_device":
			c.WhisperDevice = val
		case "opensubtitles_api_key":
			c.OpenSubtitlesAPIKey = val
		case "opensubtitles_user_agent":
			c.OpenSubtitlesUA = val
		case "session_idle_timeout_minutes":
			if v, err := strconv.Atoi(val); err == nil {
				c.SessionIdleTimeoutMinutes = v
			}
		case "torznab":
			c.Torznab = append(c.Torznab, parseIndexer(val))
		case "newznab":
			c.Newznab = append(c.Newznab, parseIndexer(val))
		}
	}
	return sc.Err()
}

func (c *Config) loadEnv() {
	setEnv(&c.TMDBAPIKey, "STREAMR_TMDB_API_KEY")
	setEnv(&c.QbitURL, "STREAMR_QBIT_URL")
	setEnv(&c.QbitUser, "STREAMR_QBIT_USER")
	setEnv(&c.QbitPass, "STREAMR_QBIT_PASS")
	setEnv(&c.QbitSaveDir, "STREAMR_QBIT_SAVE_DIR")
	setEnv(&c.Addr, "STREAMR_ADDR")
	setEnv(&c.WebDir, "STREAMR_WEB_DIR")
	setEnv(&c.DataDir, "STREAMR_DATA_DIR")
	setEnv(&c.OpenSubtitlesAPIKey, "STREAMR_OPENSUBTITLES_API_KEY")
	if os.Getenv("STREAMR_WHISPERX_ENABLED") != "" {
		c.WhisperXEnabled = parseBool(os.Getenv("STREAMR_WHISPERX_ENABLED"))
	}
}

func parseIndexer(val string) IndexerConfig {
	ic := IndexerConfig{Enabled: true}
	for _, field := range strings.Fields(val) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "name":
			ic.Name = v
		case "url":
			ic.URL = v
		case "key":
			ic.APIKey = v
		case "enabled":
			ic.Enabled = parseBool(v)
		}
	}
	return ic
}

// unquote strips a single pair of matching surrounding quotes from a value, so
// both `web_dir: "web/dist"` and `web_dir: web/dist` yield the path web/dist.
// Without this, quoted values in config.example.yaml were parsed WITH the
// quotes, producing invalid paths/addresses (e.g. a 404 because the web dir
// `"web/dist"` with quotes does not exist).
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func setEnv(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

// Validate reports configuration that would prevent core features working.
func (c *Config) Validate() []string {
	var warns []string
	if c.TMDBAPIKey == "" {
		warns = append(warns, "TMDB api key not set: search/discovery disabled")
	}
	if c.Engine == "qbittorrent" && c.QbitURL == "" {
		warns = append(warns, "engine=qbittorrent but qbit_url not set: torrent streaming disabled")
	}
	if c.Engine == "qbittorrent" && strings.HasPrefix(c.QbitURL, "http://") {
		warns = append(warns, "qbit_url is plain http://: the WebUI password is sent in cleartext — use https or a local socket")
	}
	if len(c.Torznab) == 0 && len(c.Newznab) == 0 {
		warns = append(warns, "no indexers configured: source search will return nothing")
	}
	// The web UI only works if web_dir points at the built frontend. An empty
	// or non-existent path means "/" returns 404 — the most common first-run
	// surprise — so surface it explicitly.
	if c.WebDir == "" {
		warns = append(warns, "web_dir not set: the web UI is disabled and GET / returns 404 (set web_dir: \"web/dist\" and run 'make web')")
	} else if fi, err := os.Stat(c.WebDir); err != nil || !fi.IsDir() {
		warns = append(warns, fmt.Sprintf("web_dir %q does not exist: GET / will 404 (run 'make web' to build the frontend, or fix the path)", c.WebDir))
	} else if _, err := os.Stat(c.WebDir + "/index.html"); err != nil {
		warns = append(warns, fmt.Sprintf("web_dir %q has no index.html: the frontend may not be built (run 'make web')", c.WebDir))
	}
	return warns
}

func (c *Config) String() string {
	return fmt.Sprintf("addr=%s data=%s torznab=%d newznab=%d whisperx=%v",
		c.Addr, c.DataDir, len(c.Torznab), len(c.Newznab), c.WhisperXEnabled)
}
