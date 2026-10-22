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
