package mipmap

import "errors"

// 可区分的失败原因，调用方可用 errors.Is 判定。
var (
	// ErrInvalidConfig 配置非法（Tile 尺寸、层级上限等）。
	ErrInvalidConfig = errors.New("mipmap: invalid config")
	// ErrInvalidImage 输入图像非法（尺寸为 0 或超过上限）。
	ErrInvalidImage = errors.New("mipmap: invalid image")
	// ErrOutOfRange 采样或访问坐标超出范围。
	ErrOutOfRange = errors.New("mipmap: out of range")
	// ErrCapacity 图像或层级超过内存上界，无法容纳。
	ErrCapacity = errors.New("mipmap: capacity exceeded")
)
