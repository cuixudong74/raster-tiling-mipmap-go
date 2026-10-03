# raster-tiling-mipmap-go

面向大尺寸位图的分块（tiling）与多级缩小图（mipmap pyramid）库。
生成、缓存、采样整条链路都在本进程内完成，不依赖任何外部服务，
所有结论与错误均可在本地复现。

## 快速开始

```go
src := image.NewNRGBA(image.Rect(0, 0, 5000, 3000)) // 或解码本地样本图
cfg := mipmap.DefaultConfig()
cfg.TileSize = 256
pyr, err := mipmap.Generate(ctx, src, cfg) // 支持 ctx 取消/超时
if err != nil { /* errors.Is 可区分 ErrInvalidConfig / ErrInvalidImage / context.Canceled ... */ }

s := mipmap.NewSampler(pyr)
r, g, b, a := s.SampleBilinear(1234.5, 678.9, 2.3)   // 小数层级 = 三线性过渡
r, g, b, a = s.SampleAniso(x, y, dux, duy, dvx, dvy) // 各向异性足迹采样
```

## 分块与层级语义

- **分块**：每级图像被切成 `TileSize × TileSize` 的 Tile（`TileID{Level, TX, TY}`），
  右/下边缘 Tile 可以更小。任一层的全部 Tile 拼接后**完整还原**该层图像：
  每个像素恰好被一个 Tile 覆盖，无缺块、无重叠、无未定义边缘
  （`TestTilesCoverLevelExactly` 对非二次幂尺寸逐一验证）。
- **层级**：第 0 层为原图；第 k+1 层尺寸为 `ceil(第k层 / 2)`（宽、高独立），
  逐级递减直到 `1×1` 或达到 `MaxLevels`。层级数量与每层尺寸完全由
  源图尺寸与配置决定。

## 确定性下采样

`Generate` 对同一源图，在**任意分块划分、任意并行度、任意生成顺序**下，
产出的每一级**逐像素一致**（`TestDeterministicAcrossTilingAndWorkers`）。
规则：

- 目标像素 `(x,y)` 对应上一层 `(2x,2y)` 处的 2×2 块；
- 上一层尺寸为奇数时，最后一行/列按**边缘复制**（clamp）补齐 2×2；
- 在**预乘 alpha** 空间做盒式平均，再反预乘回 NRGBA——
  全透明像素的颜色不会污染结果，透明通道与颜色通道处理统一；
- 全部使用整数运算（四舍五入），不依赖浮点求和顺序，
  每个输出像素独立计算，因此与并行度、生成顺序无关。

## 采样语义

- **坐标系**：第 0 层像素中心坐标，像素 `(i,j)` 中心为 `(i+0.5, j+0.5)`；
  层级映射 `xL = (x+0.5)/2^L − 0.5`。
- **无缝性**：插值读取通过层内统一像素访问器进行，跨 Tile 边界与跨层级的
  双线性结果与直接对整图插值**完全一致**，块边缘无亮度/透明度跳变
  （`TestSeamlessAcrossTileBoundary` / `TestSeamlessAcrossLevels`）。
- **透明通道**：插值在预乘 alpha 空间进行，与下采样规则一致。
- **层级选择与过渡**：`level` 为小数时在 `floor/ceil` 两层间线性混合
  （三线性），层级切换平滑无跳变（`TestTrilinearSmoothTransition`）；
  `level` 越界截断到 `[0, NumLevels−1]`。
- **各向异性**（`SampleAniso`，梯度为第 0 层像素单位）：
  - 长轴 `major = max(|du|,|dv|)`，短轴 `minor = min(|du|,|dv|)`；
  - 层级 `λ = clamp(log2(minor), 0, NumLevels−1)`；
  - 采样点数 `N = clamp(ceil(major/minor), 1, MaxAniso)`，沿长轴均匀分布，
    位置 `t_i = (i+0.5)/N − 0.5`，**权重均为 1/N**——数量与权重确定且可解释；
  - 退化足迹（零梯度）退化为单点采样。
- **越界规则**：所有采样坐标按**边缘复制**（clamp）截断到层范围内。

## Tile 缓存

`TileCache` 是有内存上界的 LRU 缓存，用于复用已解码的 Tile：

- 容量以字节计，达到上界时按 **LRU（最久未使用）** 淘汰；
- 单块超过总容量时不缓存（`Add` 返回 `false`），缓存占用不随请求量无界增长；
- `Metrics()` 暴露 `Hits / Misses / Evictions / Bytes`，命中与未命中可观测；
- `GetOrLoad` 对同一 Tile 的并发未命中只加载一次（singleflight），
  加载完成前等待者不可见 Tile，不会读到半成品；
- 缓存中的 Tile 约定为**不可变**，并发读写无数据竞争（`-race` 验证）。

## 并发、取消与错误

- `Generate` 内部按层并行（`Workers` 控制协程数），可安全并发调用；
- `ctx` 取消或超时：返回包装了 `context.Canceled` / `context.DeadlineExceeded`
  的错误，**不返回半成品**，内部状态全部丢弃，不污染后续请求；
- 异常输入返回可区分、可定位的错误（`errors.Is` 判定）：
  - `ErrInvalidConfig`：非法配置（含具体字段与取值）；
  - `ErrInvalidImage`：空图、nil 图、像素数超过 `MaxImagePixels`；
  - 其他：`ErrOutOfRange`、`ErrCapacity`。

## 配置项（`Config`）

| 字段 | 默认 | 说明 |
|---|---|---|
| `TileSize` | 256 | 标准 Tile 边长（像素），边缘 Tile 可更小 |
| `MaxLevels` | 0（不限） | 层级上限（含第 0 层），实际层数取「递减到 1×1」与该上限的较小者 |
| `Workers` | 4 | 并发生成的协程数，不影响结果 |
| `MaxImagePixels` | 2³⁰ | 源图最大像素数，超过报 `ErrInvalidImage` |
| `CacheBytes` | 64 MiB | Tile 缓存内存上界（供 `TileCache` 默认值参考） |
| `MaxAniso` | 8 | 各向异性采样最大采样点数 |

## 测试

```sh
go test ./... -race -count=1
```

覆盖：非二次幂尺寸、单像素图、透明通道、分块接缝、多级/三线性/各向异性
采样、缓存淘汰与容量上界、并发取消与超时、异常输入。
