package mipmap

import (
	"errors"
	"fmt"
	"runtime"
)

// Config controls mipmap generation, caching and sampling behaviour.
//
// All fields have a zero-value meaning: a zero Config is NOT valid; use
// DefaultConfig to obtain a sensible configuration and then override fields.
type Config struct {
	// TileSize is the edge length (in pixels) of a square tile. Edge tiles
	// at the right/bottom of a level may be smaller. Must be > 0.
	TileSize int

	// MaxCacheBytes is the upper bound on the number of bytes of decoded
	// tile pixel data the cache may hold. Must be >= the size of one full
	// tile (TileSize*TileSize*4), otherwise construction fails with
	// ErrInvalidConfig.
	MaxCacheBytes int64

	// MaxAnisotropy is the maximum number of anisotropic samples taken
	// along the major footprint axis. Must be >= 1. A value of 1 disables
	// anisotropic filtering.
	MaxAnisotropy int

	// Workers is the maximum number of concurrent tile generators used by
	// Generate. If <= 0 it defaults to runtime.NumCPU().
	Workers int

	// LODBias is added to the computed level-of-detail before clamping.
	// Positive values blur more; negative values sharpen.
	LODBias float64
}

// DefaultConfig returns a configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		TileSize:      256,
		MaxCacheBytes: 64 << 20, // 64 MiB
		MaxAnisotropy: 8,
		Workers:       runtime.NumCPU(),
		LODBias:       0,
	}
}

// validate checks the configuration and returns an error wrapping
// ErrInvalidConfig if any field is out of range.
func (c Config) validate() error {
	if c.TileSize <= 0 {
		return fmt.Errorf("%w: TileSize must be > 0", ErrInvalidConfig)
	}
	if c.MaxAnisotropy < 1 {
		return fmt.Errorf("%w: MaxAnisotropy must be >= 1", ErrInvalidConfig)
	}
	fullTileBytes := int64(c.TileSize) * int64(c.TileSize) * 4
	if c.MaxCacheBytes < fullTileBytes {
		return fmt.Errorf("%w: MaxCacheBytes must be >= one full tile (%d bytes)", ErrInvalidConfig, fullTileBytes)
	}
	return nil
}

// workerCount returns the effective worker count.
func (c Config) workerCount() int {
	if c.Workers > 0 {
		return c.Workers
	}
	return runtime.NumCPU()
}

var _ = errors.New
