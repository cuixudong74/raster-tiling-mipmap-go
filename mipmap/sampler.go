package mipmap

import (
	"context"
	"image/color"
	"math"
)

// Pixel is a premultiplied-alpha RGBA pixel in float64, with channels in
// [0,1]. It is the internal representation used for all filtering so that
// alpha handling is uniform across downsampling and sampling.
type Pixel struct {
	R, G, B, A float64
}

// SampleRequest describes a single texture lookup. (U,V) are normalized
// coordinates in [0,1]. The derivatives DUDX, DUDY, DVDX, DVDY describe how
// (U,V) change per screen pixel and drive level-of-detail selection and
// anisotropic filtering.
type SampleRequest struct {
	U, V          float64
	DUDX, DUDY    float64
	DVDX, DVDY    float64
	MaxAnisotropy int     // 0 to use the config default
	LODBias       float64 // NaN to use the config default
}

// levelPixelFetcher fetches a premultiplied pixel at level L and integer
// pixel coordinate (x,y). Implementations clamp (x,y) to the level bounds,
// so out-of-range footprint coordinates are handled by edge replication.
type levelPixelFetcher func(ctx context.Context, level, x, y int) (Pixel, error)

// Sampler performs seamless, deterministic texture lookups across tiles and
// mip levels.
type Sampler struct {
	m *Mipmap
}

// NewSampler returns a Sampler for the given mipmap.
func NewSampler(m *Mipmap) *Sampler { return &Sampler{m: m} }

// Sample performs a filtered lookup and returns the resulting NRGBA color.
// The result is deterministic: the same request on the same mipmap always
// produces the same color, and sampling across tile boundaries is bit-identical
// to sampling a fully assembled image.
func (s *Sampler) Sample(ctx context.Context, req SampleRequest) (color.NRGBA, error) {
	m := s.m
	fetch := m.tiledFetcher(ctx)
	acc, err := samplePixel(ctx, fetch, req, m.levelW[0], m.levelH[0], m.maxLevel, m.cfg)
	if err != nil {
		return color.NRGBA{}, err
	}
	return pixelToNRGBA(acc), nil
}

// samplePixel performs the full filtered lookup (footprint, LOD, anisotropic
// tap accumulation) using the given pixel fetcher. Both the tiled sampler and
// the reference (whole-image) path use this function, so their results are
// bit-identical by construction.
func samplePixel(
	ctx context.Context,
	fetch levelPixelFetcher,
	req SampleRequest,
	W, H, maxLevel int,
	cfg Config,
) (Pixel, error) {
	// Texel-space sample coordinate (texel-center convention: pixel i spans
	// [i,i+1) with center at i+0.5).
	fx := req.U*float64(W) - 0.5
	fy := req.V*float64(H) - 0.5

	// Footprint and level of detail.
	major, minor := computeFootprint(req, W, H)
	lambda := computeLambda(req, W, H, cfg)

	// Anisotropic ratio and deterministic sample count.
	maxAniso := req.MaxAnisotropy
	if maxAniso <= 0 {
		maxAniso = cfg.MaxAnisotropy
	}
	ratio := 1.0
	if minor > 0 {
		ratio = major / minor
	}
	ratio = clampFloat(ratio, 1, float64(maxAniso))
	n := int(math.Ceil(ratio))
	n = int(clampFloat(float64(n), 1, float64(maxAniso)))

	// Major-axis unit direction in texel space.
	dxdx := req.DUDX * float64(W)
	dxdy := req.DUDY * float64(W)
	dydx := req.DVDX * float64(H)
	dydy := req.DVDY * float64(H)
	e1 := math.Hypot(dxdx, dydx)
	e2 := math.Hypot(dxdy, dydy)
	var mdx, mdy float64
	if e1 >= e2 && e1 > 0 {
		mdx, mdy = dxdx/e1, dydx/e1
	} else if e2 > 0 {
		mdx, mdy = dxdy/e2, dydy/e2
	}

	var acc Pixel
	for i := 0; i < n; i++ {
		// Symmetric taps about the sample point, spaced one minor-axis
		// footprint apart (in level-0 texel units).
		t := float64(i) - float64(n-1)/2.0
		ox := t * minor * mdx
		oy := t * minor * mdy
		c, err := trilinear(ctx, fetch, lambda, fx+ox, fy+oy, maxLevel)
		if err != nil {
			return Pixel{}, err
		}
		acc.R += c.R
		acc.G += c.G
		acc.B += c.B
		acc.A += c.A
	}
	inv := 1.0 / float64(n)
	acc.R *= inv
	acc.G *= inv
	acc.B *= inv
	acc.A *= inv
	return acc, nil
}

// computeFootprint returns the lengths (in texels) of the major and minor
// axes of the screen-space footprint, given the texture-coordinate derivatives
// and the level-0 dimensions.
func computeFootprint(req SampleRequest, W, H int) (major, minor float64) {
	dxdx := req.DUDX * float64(W)
	dxdy := req.DUDY * float64(W)
	dydx := req.DVDX * float64(H)
	dydy := req.DVDY * float64(H)
	e1 := math.Hypot(dxdx, dydx)
	e2 := math.Hypot(dxdy, dydy)
	return math.Max(e1, e2), math.Min(e1, e2)
}

// computeLambda returns the level-of-detail for a sample request. For
// isotropic footprints major==minor, so log2(minor)==log2(major). For
// anisotropic footprints the LOD is selected from the minor axis so the minor
// axis is not over-blurred. The configured (or per-request) LOD bias is added.
func computeLambda(req SampleRequest, W, H int, cfg Config) float64 {
	major, minor := computeFootprint(req, W, H)
	lambda := math.Log2(major)
	if minor > 0 {
		lambda = math.Log2(minor)
	}
	bias := req.LODBias
	if math.IsNaN(bias) {
		bias = cfg.LODBias
	}
	return lambda + bias
}

// tiledFetcher returns a levelPixelFetcher that reads premultiplied pixels
// from tiles via the mipmap cache. Out-of-range coordinates are clamped to the
// level's edge pixels.
func (m *Mipmap) tiledFetcher(ctx context.Context) levelPixelFetcher {
	return func(_ context.Context, level, x, y int) (Pixel, error) {
		W, H := m.levelDims(level)
		x = clampInt(x, 0, W-1)
		y = clampInt(y, 0, H-1)
		ts := m.cfg.TileSize
		tx, ty := x/ts, y/ts
		t, err := m.GetTile(ctx, level, tx, ty)
		if err != nil {
			return Pixel{}, err
		}
		lx, ly := x-tx*ts, y-ty*ts
		i := ly*t.Img.Stride + lx*4
		r := float64(t.Img.Pix[i+0]) / 255.0
		g := float64(t.Img.Pix[i+1]) / 255.0
		b := float64(t.Img.Pix[i+2]) / 255.0
		a := float64(t.Img.Pix[i+3]) / 255.0
		return Pixel{R: r * a, G: g * a, B: b * a, A: a}, nil
	}
}

// bilinear performs a bilinear lookup at level L and fractional pixel
// coordinate (fx,fy). The fetcher clamps out-of-range coordinates to the
// level's edge pixels. Filtering is in premultiplied alpha.
func bilinear(ctx context.Context, fetch levelPixelFetcher, level int, fx, fy float64) (Pixel, error) {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	wx := fx - float64(x0)
	wy := fy - float64(y0)

	p00, err := fetch(ctx, level, x0, y0)
	if err != nil {
		return Pixel{}, err
	}
	p10, err := fetch(ctx, level, x0+1, y0)
	if err != nil {
		return Pixel{}, err
	}
	p01, err := fetch(ctx, level, x0, y0+1)
	if err != nil {
		return Pixel{}, err
	}
	p11, err := fetch(ctx, level, x0+1, y0+1)
	if err != nil {
		return Pixel{}, err
	}

	w00 := (1 - wx) * (1 - wy)
	w10 := wx * (1 - wy)
	w01 := (1 - wx) * wy
	w11 := wx * wy
	return Pixel{
		R: p00.R*w00 + p10.R*w10 + p01.R*w01 + p11.R*w11,
		G: p00.G*w00 + p10.G*w10 + p01.G*w01 + p11.G*w11,
		B: p00.B*w00 + p10.B*w10 + p01.B*w01 + p11.B*w11,
		A: p00.A*w00 + p10.A*w10 + p01.A*w01 + p11.A*w11,
	}, nil
}

// trilinear blends between two adjacent mip levels at the given fractional
// level-of-detail. lambda is clamped to [0,maxLevel].
func trilinear(ctx context.Context, fetch levelPixelFetcher, lambda, fx, fy float64, maxLevel int) (Pixel, error) {
	lambda = clampFloat(lambda, 0, float64(maxLevel))
	lf := math.Floor(lambda)
	lc := math.Ceil(lambda)
	if lf == lc {
		return bilinear(ctx, fetch, int(lf), fx, fy)
	}
	frac := lambda - lf
	// Level-L pixel coordinates are level-0 coordinates divided by 2^L.
	scale0 := math.Pow(2, float64(lf))
	scale1 := math.Pow(2, float64(lc))
	c0, err := bilinear(ctx, fetch, int(lf), fx/scale0, fy/scale0)
	if err != nil {
		return Pixel{}, err
	}
	c1, err := bilinear(ctx, fetch, int(lc), fx/scale1, fy/scale1)
	if err != nil {
		return Pixel{}, err
	}
	return Pixel{
		R: c0.R*(1-frac) + c1.R*frac,
		G: c0.G*(1-frac) + c1.G*frac,
		B: c0.B*(1-frac) + c1.B*frac,
		A: c0.A*(1-frac) + c1.A*frac,
	}, nil
}

// pixelToNRGBA converts a premultiplied float Pixel back to non-premultiplied
// NRGBA. Fully transparent pixels map to {0,0,0,0}.
func pixelToNRGBA(p Pixel) color.NRGBA {
	a := clampFloat(p.A, 0, 1)
	if a <= 0 {
		return color.NRGBA{0, 0, 0, 0}
	}
	return color.NRGBA{
		R: clampRound(p.R / a * 255),
		G: clampRound(p.G / a * 255),
		B: clampRound(p.B / a * 255),
		A: clampRound(a * 255),
	}
}

// clampFloat clamps v to [lo,hi].
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
