// Package mipmap provides deterministic, tiled mipmap generation and sampling
// for large raster images.
//
// # Overview
//
// A Source image (level 0) is split into fixed-size square tiles. A pyramid of
// progressively downsampled levels is built on top. Every tile of every level
// is generated independently and cached in a bounded, thread-safe LRU cache.
// Tiles are immutable once created, so concurrent readers never observe a
// half-written tile.
//
// # Determinism
//
// Downsampling is a pure function of the source pixels: each level-L pixel is
// a 2x2 box average of the four corresponding level-(L-1) pixels, with edge
// clamping. Because each level is computed solely from the previous level's
// pixels, the result is identical regardless of tile division, parallelism or
// generation order.
//
// # Alpha handling
//
// All filtering (downsampling and sampling) is performed in premultiplied
// alpha (r*a, g*a, b*a, a) with float64 precision, then converted back to
// non-premultiplied NRGBA. This avoids dark fringes at alpha edges. Fully
// transparent pixels (a==0) are stored as {0,0,0,0}.
//
// # Seamless sampling
//
// The sampler performs bilinear, trilinear and anisotropic filtering. A single
// bilinear tap gathers pixels from up to four tiles, so interpolation across a
// tile boundary is bit-identical to interpolation on a fully assembled image.
// Out-of-range footprint coordinates are clamped to the level's edge pixels
// (edge replication).
//
// # Level-of-detail
//
// The screen-space footprint is computed from the texture-coordinate
// derivatives. For isotropic filtering, lambda = log2(majorFootprintAxis).
// For anisotropic filtering, lambda = log2(minorFootprintAxis) and
// ceil(major/minor) taps are taken along the major axis, each with equal
// weight. Sample count and weights are fully determined by the derivatives and
// the configured maximum anisotropy. Trilinear blending between adjacent levels
// gives smooth LOD transitions.
//
// # Configuration
//
// See Config for the tunable parameters: TileSize, MaxCacheBytes,
// MaxAnisotropy, Workers and LODBias. Use DefaultConfig for sensible defaults.
package mipmap
