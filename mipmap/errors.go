package mipmap

import "errors"

// Typed errors returned by the mipmap package. Use errors.Is / errors.As to
// distinguish failure causes. All errors are wrapped with context so the
// underlying cause is locatable.
var (
	// ErrInvalidConfig is returned when a Config field is out of range.
	ErrInvalidConfig = errors.New("invalid config")

	// ErrInvalidImage is returned when the source image is nil or has zero
	// width or height.
	ErrInvalidImage = errors.New("invalid source image")

	// ErrInvalidLevel is returned when a mip level is outside [0, maxLevel].
	ErrInvalidLevel = errors.New("invalid mip level")

	// ErrInvalidTile is returned when a tile coordinate is outside the grid
	// for the requested level.
	ErrInvalidTile = errors.New("invalid tile coordinate")

	// ErrCacheFull is returned when a single tile cannot fit in the cache
	// even after evicting every other entry.
	ErrCacheFull = errors.New("cache capacity too small for tile")

	// ErrCancelled is returned when generation is cancelled or times out
	// before a tile could be produced.
	ErrCancelled = errors.New("generation cancelled")
)
