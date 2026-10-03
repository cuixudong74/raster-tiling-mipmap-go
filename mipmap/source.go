package mipmap

import (
	"image"
	"image/color"
)

// Source is the level-0 image from which the mipmap chain is built. It holds
// an NRGBA (non-premultiplied, 8-bit) pixel buffer. Source images are
// immutable once constructed.
type Source struct {
	Img *image.NRGBA
	W   int
	H   int
}

// NewSourceFromImage converts any image.Image to an NRGBA Source. The
// returned Source owns its pixel buffer; the input is not retained.
func NewSourceFromImage(img image.Image) (*Source, error) {
	if img == nil {
		return nil, ErrInvalidImage
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, ErrInvalidImage
	}
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nrgba.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return &Source{Img: nrgba, W: w, H: h}, nil
}

// NewSource builds a Source from a pixel-generating function. fill is called
// for every pixel (x,y) in [0,w)x[0,h). This is the in-process way to create
// test images without external files.
func NewSource(w, h int, fill func(x, y int) color.NRGBA) (*Source, error) {
	if w <= 0 || h <= 0 {
		return nil, ErrInvalidImage
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, fill(x, y))
		}
	}
	return &Source{Img: img, W: w, H: h}, nil
}

// NewSolidSource returns a Source filled with a single color.
func NewSolidSource(w, h int, c color.NRGBA) (*Source, error) {
	return NewSource(w, h, func(_, _ int) color.NRGBA { return c })
}

// NewCheckerSource returns a Source with a checkerboard pattern of two
// colors. square is the edge length of each checker square.
func NewCheckerSource(w, h, square int, a, b color.NRGBA) (*Source, error) {
	if square <= 0 {
		square = 1
	}
	return NewSource(w, h, func(x, y int) color.NRGBA {
		if ((x/square)+(y/square))%2 == 0 {
			return a
		}
		return b
	})
}

// NewGradientSource returns a Source with a horizontal gradient from left to
// right, interpolating between two colors.
func NewGradientSource(w, h int, a, b color.NRGBA) (*Source, error) {
	return NewSource(w, h, func(x, _ int) color.NRGBA {
		t := float64(x) / float64(w-1)
		if w == 1 {
			t = 0
		}
		return lerpNRGBA(a, b, t)
	})
}

// lerpNRGBA linearly interpolates between two NRGBA colors.
func lerpNRGBA(a, b color.NRGBA, t float64) color.NRGBA {
	return color.NRGBA{
		R: uint8(float64(a.R) + t*(float64(b.R)-float64(a.R))),
		G: uint8(float64(a.G) + t*(float64(b.G)-float64(a.G))),
		B: uint8(float64(a.B) + t*(float64(b.B)-float64(a.B))),
		A: uint8(float64(a.A) + t*(float64(b.A)-float64(a.A))),
	}
}
