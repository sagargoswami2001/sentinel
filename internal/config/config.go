// Package config loads and validates the sentinel YAML configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults are applied to any target that doesn't set its own values.
type Defaults struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
	ExpectStatus   int `yaml:"expect_status"`
}

// Target is one endpoint sentinel watches.
type Target struct {
	Name           string `yaml:"name"`
	URL            string `yaml:"url"`
	ExpectStatus   int    `yaml:"expect_status"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Timeout returns the target's timeout as a time.Duration.
func (t Target) Timeout() time.Duration {
	return time.Duration(t.TimeoutSeconds) * time.Second
}

// Channel is one alerting destination: a platform key (slack, teams,
// googlechat, discord, webhook) plus its webhook URL.
type Channel struct {
	Platform string `yaml:"platform"`
	URL      string `yaml:"url"`
}

// Notify holds any number of alerting channels. Env vars
// SENTINEL_SLACK_WEBHOOK / SENTINEL_TEAMS_WEBHOOK add channels on top
// of the file, so secrets can stay out of committed YAML.
type Notify struct {
	Channels []Channel `yaml:"channels"`
}

// Config is the root of the YAML file.
type Config struct {
	Defaults Defaults `yaml:"defaults"`
	Notify   Notify   `yaml:"notify"`
	Targets  []Target `yaml:"targets"`
}

// Load reads the YAML file at path, applies defaults, and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	// An empty target list is fine — the dashboard starts with a clean
	// board and targets are added from the browser.

	// Env vars add channels on top of the file, for secrets.
	if v := os.Getenv("SENTINEL_SLACK_WEBHOOK"); v != "" {
		cfg.Notify.Channels = append(cfg.Notify.Channels, Channel{Platform: "slack", URL: v})
	}
	if v := os.Getenv("SENTINEL_TEAMS_WEBHOOK"); v != "" {
		cfg.Notify.Channels = append(cfg.Notify.Channels, Channel{Platform: "teams", URL: v})
	}

	// Fill in per-target gaps from defaults, then from hard-coded fallbacks.
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		if t.URL == "" {
			return nil, fmt.Errorf("%s: target %q has no url", path, t.Name)
		}
		if t.Name == "" {
			t.Name = t.URL
		}
		if t.ExpectStatus == 0 {
			t.ExpectStatus = cfg.Defaults.ExpectStatus
		}
		if t.ExpectStatus == 0 {
			t.ExpectStatus = 200
		}
		if t.TimeoutSeconds == 0 {
			t.TimeoutSeconds = cfg.Defaults.TimeoutSeconds
		}
		if t.TimeoutSeconds == 0 {
			t.TimeoutSeconds = 10
		}
	}

	return &cfg, nil
}

// Save writes the config back to disk — the reverse of Load. Used by
// the dashboard so targets added or removed in the browser survive a
// restart. Hand-written comments in the file are not preserved.
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
