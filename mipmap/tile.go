package mipmap

import (
	"image"
)

// Tile is a fixed-size square chunk of a single mip level. Edge tiles may be
// smaller than the configured TileSize. Tiles are immutable once created: the
// cache only ever publishes fully-constructed tiles, so concurrent readers
// never observe a half-written tile.
type Tile struct {
	Level int
	TX    int // tile column
	TY    int // tile row
	Img   *image.NRGBA
}

// W returns the tile's pixel width.
func (t *Tile) W() int { return t.Img.Rect.Dx() }

// H returns the tile's pixel height.
func (t *Tile) H() int { return t.Img.Rect.Dy() }

// Bytes returns the number of bytes of pixel data the tile holds.
func (t *Tile) Bytes() int64 { return int64(t.W() * t.H() * 4) }

// tileKey identifies a tile within the mipmap.
type tileKey struct {
	level int
	tx    int
	ty    int
}

// tileDims computes the pixel dimensions of tile (tx,ty) at the given level,
// given the level's total dimensions and the configured tile size.
func tileDims(levelW, levelH, tileSize, tx, ty int) (w, h int) {
	x0 := tx * tileSize
	y0 := ty * tileSize
	w = levelW - x0
	if w > tileSize {
		w = tileSize
	}
	h = levelH - y0
	if h > tileSize {
		h = tileSize
	}
	return w, h
}
