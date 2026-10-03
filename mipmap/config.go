package mipmap

import "fmt"

// Config 控制分块与金字塔生成的全部参数。
type Config struct {
	// TileSize 标准 Tile 边长（像素），必须为正。边缘 Tile 可以更小。
	TileSize int
	// MaxLevels 层级上限（含第 0 层原图）。0 表示不限制，
	// 实际层数由源图尺寸决定（递减到 1x1 为止）。
	MaxLevels int
	// Workers 并发生成的工作协程数。<=0 时取默认值 4。
	Workers int
	// MaxImagePixels 源图允许的最大像素数，超过则报 ErrInvalidImage，
	// 防止异常输入撑爆内存。0 表示使用默认值。
	MaxImagePixels int64
	// CacheBytes 已解码 Tile 缓存的内存上界（字节）。0 表示使用默认值。
	CacheBytes int64
	// MaxAniso 各向异性采样的最大采样点数。<=0 时取默认值 8。
	MaxAniso int
}

// DefaultConfig 返回一组保守的默认配置。
func DefaultConfig() Config {
	return Config{
		TileSize:       256,
		MaxLevels:      0,
		Workers:        4,
		MaxImagePixels: 1 << 30,
		CacheBytes:     64 << 20,
		MaxAniso:       8,
	}
}

// withDefaults 填充零值字段。
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.TileSize == 0 {
		c.TileSize = d.TileSize
	}
	if c.Workers <= 0 {
		c.Workers = d.Workers
	}
	if c.MaxImagePixels == 0 {
		c.MaxImagePixels = d.MaxImagePixels
	}
	if c.CacheBytes == 0 {
		c.CacheBytes = d.CacheBytes
	}
	if c.MaxAniso <= 0 {
		c.MaxAniso = d.MaxAniso
	}
	return c
}

// Validate 校验配置，返回可定位到具体字段的错误。
func (c Config) Validate() error {
	if c.TileSize < 0 {
		return fmt.Errorf("%w: TileSize=%d must be >= 0", ErrInvalidConfig, c.TileSize)
	}
	if c.MaxLevels < 0 {
		return fmt.Errorf("%w: MaxLevels=%d must be >= 0", ErrInvalidConfig, c.MaxLevels)
	}
	if c.CacheBytes < 0 {
		return fmt.Errorf("%w: CacheBytes=%d must be >= 0", ErrInvalidConfig, c.CacheBytes)
	}
	if c.MaxImagePixels < 0 {
		return fmt.Errorf("%w: MaxImagePixels=%d must be >= 0", ErrInvalidConfig, c.MaxImagePixels)
	}
	return nil
}
