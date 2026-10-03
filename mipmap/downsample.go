package mipmap

import (
	"image/color"
	"math"
)

// downsamplePixel computes one output pixel of a 2x box downsample. The four
// input pixels are the level-(L-1) pixels covering the output pixel at level
// L. They are gathered by the caller (with edge clamping already applied), so
// this function is a pure function of its inputs: the same four pixels always
// produce the same result, regardless of tile boundaries, generation order or
// parallelism.
//
// Math: pixels are converted to premultiplied alpha (r*a, g*a, b*a, a) in
// float64, averaged, then converted back to non-premultiplied NRGBA. This
// avoids dark fringes at alpha edges. Fully transparent inputs (a==0)
// produce {0,0,0,0}.
func downsamplePixel(p [4]color.NRGBA) color.NRGBA {
	var r, g, b, a float64
	for _, q := range p {
		af := float64(q.A) / 255.0
		r += float64(q.R) * af
		g += float64(q.G) * af
		b += float64(q.B) * af
		a += af
	}
	r /= 4
	g /= 4
	b /= 4
	a /= 4
	if a <= 0 {
		return color.NRGBA{0, 0, 0, 0}
	}
	return color.NRGBA{
		R: clampRound(r / a),
		G: clampRound(g / a),
		B: clampRound(b / a),
		A: clampRound(a * 255),
	}
}

// clampRound rounds v to the nearest integer and clamps to [0,255].
func clampRound(v float64) uint8 {
	v = math.Round(v)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// clampInt clamps v to [lo,hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
