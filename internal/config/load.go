package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load decodes and validates the run configuration at path. Relative schema
// paths are resolved against the directory containing path.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("load config %q: %w (see docs/reference/config.md)", path, err)
	}

	cfg.applyDefaults()
	cfg.resolveRelativePaths(filepath.Dir(path))

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}

	return cfg, nil
}

func (c *Config) resolveRelativePaths(baseDir string) {
	c.Target.Schema.Migrations = resolvePath(baseDir, c.Target.Schema.Migrations)
	c.Target.Schema.Seed = resolvePath(baseDir, c.Target.Schema.Seed)
}

func resolvePath(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}
