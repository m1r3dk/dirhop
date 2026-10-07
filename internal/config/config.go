package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const AppName = "dirhop"

// Shard-count bounds. These mirror internal/shard (the authority on routing) but
// are duplicated here so the low-level config package stays dependency-free. A
// test asserts they stay in sync with internal/shard.
const (
	minShardCount     = 1
	maxShardCount     = 32
	defaultShardCount = 16
)

// Paths contains all application-owned paths.
type Paths struct {
	DataDir    string
	ConfigDir  string
	CacheDir   string
	Database   string
	ShardDir   string
	ConfigFile string
	History    string
}

// Config contains conservative runtime defaults. It intentionally has no
// dependency on a configuration framework so it is also usable by libraries.
type Config struct {
	Paths             Paths
	CrawlConcurrency  int
	DownloadWorkers   int
	HTTPTimeout       time.Duration
	BusyTimeout       time.Duration
	PreflightTimeout  time.Duration
	Retries           int
	DownloadDirectory string
	Metadata          string
	Color             bool
	UserAgent         string
	// ShardCount is the number of entry-storage shards across which buckets are
	// distributed so many scans can write concurrently (see
	// docs/adr/0001-sharded-storage.md). Valid range is [1, 32]; the default is
	// 16. It is fixed for a dataset once chosen; changing it needs an explicit
	// re-shard migration, never an implicit remap.
	ShardCount int
	// GrayHatWarfareAPIKey authorizes the `ghw` public-bucket search. It can
	// also come from the GRAYHATWARFARE_API_KEY environment variable, which
	// takes precedence so a key never has to be written to disk.
	GrayHatWarfareAPIKey string
	// GrayHatWarfareBaseURL overrides the API root (GRAYHATWARFARE_BASE_URL).
	// Empty means the client's documented default. Used for tests and any
	// future self-hosted/proxied endpoint.
	GrayHatWarfareBaseURL string
}

// DefaultPaths returns platform-native user paths without creating them.
func DefaultPaths() (Paths, error) {
	data, err := userDataDir()
	if err != nil {
		return Paths{}, err
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config directory: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("cache directory: %w", err)
	}
	data = filepath.Join(data, AppName)
	configDir = filepath.Join(configDir, AppName)
	cacheDir = filepath.Join(cacheDir, AppName)
	return Paths{
		DataDir: data, ConfigDir: configDir, CacheDir: cacheDir,
		Database:   filepath.Join(data, "dirhop.db"),
		ShardDir:   filepath.Join(data, "shards"),
		ConfigFile: filepath.Join(configDir, "config.toml"),
		History:    filepath.Join(data, "history"),
	}, nil
}

func userDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
	}
	if runtime.GOOS != "darwin" {
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return v, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home directory: %w", err)
		}
		return filepath.Join(home, ".local", "share"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support"), nil
}

// Default returns production-safe defaults.
func Default() (Config, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Paths: paths, CrawlConcurrency: 8, DownloadWorkers: 4,
		HTTPTimeout: 30 * time.Second, BusyTimeout: 60 * time.Second,
		PreflightTimeout: 8 * time.Second,
		Retries:          3, DownloadDirectory: "downloads", Metadata: "normal",
		Color: true, UserAgent: "dirhop/1", ShardCount: defaultShardCount,
	}, nil
}

// Load reads an optional key=value config file. Unknown keys are rejected to
// catch typos. A missing file is equivalent to defaults.
func Load(path string) (Config, error) {
	cfg, err := Default()
	if err != nil {
		return Config{}, err
	}
	if path == "" {
		path = cfg.Paths.ConfigFile
	}
	cfg.Paths.ConfigFile = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		applyEnvOverrides(&cfg)
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	for n, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		// [section] headers are accepted for TOML compatibility: keys are
		// unique across sections, so "[crawl]\nworkers=8" style files work.
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("config line %d: expected key = value", n+1)
		}
		key, value = strings.TrimSpace(key), unquote(strings.TrimSpace(value))
		if alias, ok := keyAliases[key]; ok {
			key = alias
		}
		switch key {
		case "database":
			cfg.Paths.Database = expandHome(value)
		case "shard_dir":
			cfg.Paths.ShardDir = expandHome(value)
		case "shard_count":
			cfg.ShardCount, err = positiveInt(value)
		case "history":
			cfg.Paths.History = expandHome(value)
		case "crawl_concurrency":
			cfg.CrawlConcurrency, err = positiveInt(value)
		case "download_workers":
			cfg.DownloadWorkers, err = positiveInt(value)
		case "http_timeout":
			cfg.HTTPTimeout, err = time.ParseDuration(value)
		case "busy_timeout":
			cfg.BusyTimeout, err = time.ParseDuration(value)
		case "preflight_timeout":
			cfg.PreflightTimeout, err = time.ParseDuration(value)
		case "retries":
			cfg.Retries, err = nonNegativeInt(value)
		case "download_directory":
			cfg.DownloadDirectory = expandHome(value)
		case "metadata":
			if value != "minimal" && value != "normal" && value != "full" {
				err = errors.New("must be minimal, normal, or full")
			} else {
				cfg.Metadata = value
			}
		case "color":
			cfg.Color, err = strconv.ParseBool(value)
		case "user_agent":
			cfg.UserAgent = value
		case "grayhatwarfare_api_key":
			cfg.GrayHatWarfareAPIKey = value
		case "grayhatwarfare_base_url":
			cfg.GrayHatWarfareBaseURL = value
		default:
			return Config{}, fmt.Errorf("config line %d: unknown key %q", n+1, key)
		}
		if err != nil {
			return Config{}, fmt.Errorf("config line %d (%s): %w", n+1, key, err)
		}
	}
	// The environment wins so a key never has to be committed to disk.
	applyEnvOverrides(&cfg)
	if cfg.HTTPTimeout <= 0 || cfg.BusyTimeout <= 0 {
		return Config{}, errors.New("timeouts must be positive")
	}
	if cfg.PreflightTimeout <= 0 {
		cfg.PreflightTimeout = 8 * time.Second
	}
	if cfg.ShardCount == 0 {
		cfg.ShardCount = defaultShardCount
	}
	if cfg.ShardCount < minShardCount || cfg.ShardCount > maxShardCount {
		return Config{}, fmt.Errorf("shard_count must be between %d and %d", minShardCount, maxShardCount)
	}
	if cfg.Paths.ShardDir == "" {
		cfg.Paths.ShardDir = filepath.Join(filepath.Dir(cfg.Paths.Database), "shards")
	}
	return cfg, nil
}

// applyEnvOverrides lets environment variables override file/default values for
// secrets, so an API key need never be written to the config file.
func applyEnvOverrides(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("GRAYHATWARFARE_API_KEY")); v != "" {
		cfg.GrayHatWarfareAPIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("GRAYHATWARFARE_BASE_URL")); v != "" {
		cfg.GrayHatWarfareBaseURL = v
	}
}

// EnsureDirs creates application-owned directories with user-only permissions.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.DataDir, p.ConfigDir, p.CacheDir, filepath.Dir(p.Database), p.ShardDir, filepath.Dir(p.History)} {
		if dir == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func positiveInt(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return n, nil
}

func nonNegativeInt(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return n, nil
}

func expandHome(v string) string {
	if v == "~" || strings.HasPrefix(v, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(v, "~/"))
		}
	}
	return v
}

// keyAliases maps friendly names from the README/example to canonical keys.
var keyAliases = map[string]string{
	"workers":       "crawl_concurrency",
	"timeout":       "http_timeout",
	"retry_count":   "retries",
	"download_dir":  "download_directory",
	"metadata_mode": "metadata",
	"useragent":     "user_agent",
}

// stripComment removes a trailing # comment that is not inside quotes.
func stripComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
}

// unquote strips matching TOML string quotes; basic strings honor \" and \\.
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
	}
	return v
}
