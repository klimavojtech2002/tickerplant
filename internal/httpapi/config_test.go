package httpapi

import (
	"errors"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/delivery"
)

func TestNewRefusesBadConfig(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	good := Config{Hub: hub, Engine: &stubEngine{}, PriceScale: 2, SizeScale: 3}

	for _, tc := range []struct {
		name string
		mod  func(*Config)
		ok   bool
	}{
		{"valid", func(*Config) {}, true},
		{"scales at the low corner", func(c *Config) { c.PriceScale, c.SizeScale = 0, 0 }, true},
		{"scales at the high corner", func(c *Config) { c.PriceScale, c.SizeScale = 18, 18 }, true},
		{"nil hub", func(c *Config) { c.Hub = nil }, false},
		{"nil engine", func(c *Config) { c.Engine = nil }, false},
		{"price scale below range", func(c *Config) { c.PriceScale = -1 }, false},
		{"price scale above range", func(c *Config) { c.PriceScale = 19 }, false},
		{"size scale below range", func(c *Config) { c.SizeScale = -1 }, false},
		{"size scale above range", func(c *Config) { c.SizeScale = 19 }, false},
		{"mode synthetic", func(c *Config) { c.Mode = "synthetic" }, true},
		{"mode live", func(c *Config) { c.Mode = "live" }, true},
		{"mode empty", func(c *Config) { c.Mode = "" }, true},
		{"mode in the wrong case", func(c *Config) { c.Mode = "Live" }, false},
		{"mode unknown is not a setting", func(c *Config) { c.Mode = "unknown" }, false},
		{"mode something else", func(c *Config) { c.Mode = "batch" }, false},
		{"depth zero", func(c *Config) { c.Depth = 0 }, true},
		{"depth negative", func(c *Config) { c.Depth = -1 }, false},
		{"max streams default", func(c *Config) { c.MaxStreams = 0 }, true},
		{"max streams one", func(c *Config) { c.MaxStreams = 1 }, true},
		{"max streams negative", func(c *Config) { c.MaxStreams = -1 }, false},
		{"cors origin star", func(c *Config) { c.CORSOrigin = "*" }, true},
		{"cors origin one origin", func(c *Config) { c.CORSOrigin = "https://app.example" }, true},
		{"cors origin with a path", func(c *Config) { c.CORSOrigin = "https://app.example/x" }, false},
		{"cors origin default port", func(c *Config) { c.CORSOrigin = "https://app.example:443" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.mod(&cfg)
			h, err := New(cfg)
			if tc.ok {
				if err != nil || h == nil {
					t.Fatalf("New = %v, %v; want a handler", h, err)
				}
				return
			}
			if !errors.Is(err, ErrConfig) || h != nil {
				t.Fatalf("New = %v, %v; want a nil handler and an ErrConfig error", h, err)
			}
		})
	}
}
