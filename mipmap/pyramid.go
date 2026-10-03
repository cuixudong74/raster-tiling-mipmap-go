package mipmap

import "image"

// TileID 唯一标识金字塔中的一块 Tile。
type TileID struct {
	Level int // 层级，0 为原图
	TX    int // 列号
	TY    int // 行号
}

// Tile 是一块已解码的图像分块，内容不可变，可安全并发读取。
type Tile struct {
	ID   TileID
	Rect image.Rectangle // 在该层图像中的实际覆盖区域（边缘 Tile 较小）
	Pix  []uint8         // NRGBA，按行优先紧密排列
}

// LevelInfo 描述某一层的尺寸与分块划分。
type LevelInfo struct {
	Width    int
	Height   int
	TileCols int
	TileRows int
}

// TileRect 返回 Tile (tx,ty) 在该层中的覆盖区域。
func (li LevelInfo) TileRect(tileSize, tx, ty int) image.Rectangle {
	x0 := tx * tileSize
	y0 := ty * tileSize
	x1 := x0 + tileSize
	y1 := y0 + tileSize
	if x1 > li.Width {
		x1 = li.Width
	}
	if y1 > li.Height {
		y1 = li.Height
	}
	return image.Rect(x0, y0, x1, y1)
}

// Pyramid 是生成完成的多级分块图像，只读，可并发使用。
type Pyramid struct {
	cfg    Config
	levels []LevelInfo
	tiles  [][]Tile // tiles[level][row*cols+col]
}

// NumLevels 返回层级数量。
func (p *Pyramid) NumLevels() int { return len(p.levels) }

// Level 返回第 i 层信息。
func (p *Pyramid) Level(i int) LevelInfo { return p.levels[i] }

// Tile 返回指定 Tile，不存在时返回 nil。
func (p *Pyramid) Tile(id TileID) *Tile {
	if id.Level < 0 || id.Level >= len(p.levels) {
		return nil
	}
	li := p.levels[id.Level]
	if id.TX < 0 || id.TX >= li.TileCols || id.TY < 0 || id.TY >= li.TileRows {
		return nil
	}
	t := &p.tiles[id.Level][id.TY*li.TileCols+id.TX]
	return t
}

// computeLevels 由源图尺寸与配置推导各层尺寸与分块划分。
// 规则：level(k+1) 尺寸 = ceil(level(k) / 2)，直到 1x1 或达到 MaxLevels。
func computeLevels(width, height int, cfg Config) []LevelInfo {
	var out []LevelInfo
	w, h := width, height
	for {
		out = append(out, LevelInfo{
			Width:    w,
			Height:   h,
			TileCols: ceilDiv(w, cfg.TileSize),
			TileRows: ceilDiv(h, cfg.TileSize),
		})
		if (w == 1 && h == 1) || (cfg.MaxLevels > 0 && len(out) >= cfg.MaxLevels) {
			break
		}
		w = ceilDiv(w, 2)
		h = ceilDiv(h, 2)
	}
	return out
}

func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}
