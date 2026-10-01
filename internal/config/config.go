package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultAPIPort       = 5020
	defaultCallRetention = 90 * 24 * time.Hour
)

type Config struct {
	DB          DB          `yaml:"db"`
	CallHistory CallHistory `yaml:"call_history"`
	API         API         `yaml:"api"`
}

type DB struct {
	Path string `yaml:"path"`
}

type CallHistory struct {
	Retention time.Duration `yaml:"retention"`
}

type API struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if cfg.CallHistory.Retention == 0 {
		cfg.CallHistory.Retention = defaultCallRetention
	}

	if cfg.API.Port == 0 {
		cfg.API.Port = defaultAPIPort
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) validate() error {
	switch {
	case c.DB.Path == "":
		return errors.New("db.path is required")
	case c.CallHistory.Retention < 0:
		return errors.New("call_history.retention must be positive")
	case !c.API.Address.IsValid():
		return errors.New("api.address is required")
	case c.API.Port < 1 || c.API.Port > 65535:
		return fmt.Errorf("api.port %d is out of range", c.API.Port)
	}

	return nil
}
