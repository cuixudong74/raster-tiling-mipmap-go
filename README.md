# raster-tiling-mipmap-go

Deterministic, tiled mipmap generation and sampling for large raster images in
pure Go. The whole pipeline runs in-process with no external services.

## Features

- **Tiled mipmap pyramid**: source images are split into fixed-size square
  tiles; each level is a 2x box downsample of the previous level. Level count
  and per-level dimensions are derived from the source size and configuration.
  Every tile of every level is generated independently and cached.
- **Deterministic downsampling**: each level-L pixel is a pure function of the
  corresponding level-(L-1) pixels (2x2 box average, edge-clamped). The result
  is bit-identical regardless of tile division, parallelism or generation order.
- **Seamless sampling**: bilinear, trilinear and anisotropic filtering gather
  pixels from up to four tiles per tap, so interpolation across a tile boundary
  is bit-identical to interpolation on a fully assembled image.
- **Bounded LRU cache**: decoded tiles are cached in a thread-safe LRU cache
  with a byte cap. Concurrent generation of the same tile is deduplicated
  (singleflight). Tiles are immutable once published, so readers never observe
  a half-written tile. Hits, misses and evictions are observable.
- **Concurrent generation with cancellation**: `Generate` builds the pyramid
  level by level with a bounded worker pool. Cancellation or timeout returns
  `ErrCancelled` and leaves no partial output; subsequent requests are
  unaffected.
- **Typed errors**: all failures are returned as distinguishable, locatable
  errors (`ErrInvalidConfig`, `ErrInvalidImage`, `ErrInvalidLevel`,
  `ErrInvalidTile`, `ErrCacheFull`, `ErrCancelled`).

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "image/color"

    "github.com/cuixudong74/raster-tiling-mipmap-go/mipmap"
)

func main() {
    // Build a source image in-process (or use NewSourceFromImage).
    src, _ := mipmap.NewCheckerSource(512, 512, 8,
        color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255})

    cfg := mipmap.DefaultConfig()
    cfg.TileSize = 256
    m, err := mipmap.NewMipmap(src, cfg)
    if err != nil {
        panic(err)
    }

    // Eagerly generate the whole pyramid (optional; tiles also generate lazily).
    if err := m.Generate(context.Background()); err != nil {
        panic(err)
    }

    // Sample with a screen-space footprint.
    s := mipmap.NewSampler(m)
    c, err := s.Sample(context.Background(), mipmap.SampleRequest{
        U: 0.5, V: 0.5,
        DUDX: 0.01, DVDY: 0.01, // one texel per screen pixel
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(c)
}
```

## Configuration

| Field           | Default   | Description                                                        |
|-----------------|-----------|--------------------------------------------------------------------|
| `TileSize`      | 256       | Edge length of a square tile. Edge tiles may be smaller. Must > 0. |
| `MaxCacheBytes` | 64 MiB    | Upper bound on cached tile pixel data. Must fit one full tile.    |
| `MaxAnisotropy` | 8         | Max anisotropic taps along the major footprint axis. >= 1.        |
| `Workers`       | NumCPU    | Max concurrent tile generators used by `Generate`.                |
| `LODBias`       | 0         | Added to the level-of-detail before clamping.                      |

## Semantics

### Level dimensions

Levels use ceil division so every source pixel contributes to every level:

```
W_L = ceil(W_0 / 2^L)
H_L = ceil(H_0 / 2^L)
```

The highest level is the first where both dimensions are 1:

```
maxLevel = ceil(log2(max(W_0, H_0)))
```

For example, a 5x3 source yields levels 5x3, 3x2, 2x1, 1x1.

### Alpha handling

All filtering (downsampling and sampling) is performed in premultiplied alpha
(r*a, g*a, b*a, a) with float64 precision, then converted back to
non-premultiplied NRGBA. This avoids dark fringes at alpha edges. Fully
transparent pixels (a==0) are stored as `{0,0,0,0}`.

### Edge handling

Out-of-range pixel coordinates are clamped to the level's edge pixels (edge
replication). This applies to downsampling parent gathers and to sampling
footprints, so the result is always defined and never reads uninitialized
memory.

### Level-of-detail

The screen-space footprint is computed from the texture-coordinate derivatives.
For isotropic footprints, `lambda = log2(majorAxis)`. For anisotropic
footprints, `lambda = log2(minorAxis)` and `ceil(major/minor)` taps are taken
along the major axis, each with equal weight `1/n`. Sample count and weights
are fully determined by the derivatives and `MaxAnisotropy`. Trilinear blending
between adjacent levels gives smooth LOD transitions. `lambda` is clamped to
`[0, maxLevel]`.

## Testing

```sh
go test ./...          # unit tests
go test -race ./...    # with the race detector
```

Tests cover non-power-of-two sizes, single-pixel images, alpha channels, tile
seams, mip sampling, cache eviction, concurrent cancellation and invalid input.
