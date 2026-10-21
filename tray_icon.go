package main

import "encoding/binary"

// buildTrayIcon 生成托盘用的 ICO 图标（16px 与 32px 两个尺寸）。
//
// 为什么自己拼字节：systray 在 Windows 上要求 ICO 格式，而项目里只有
// appicon.png；本地没有图像库可依赖，为了一个小图标引入图片处理链路
// 不值得。这里直接按 ICO 规范写头 + 32bpp 位图 + AND 掩码。
//
// 图形与应用图标一致：实心圆底 + 白色粗体 M。
// 托盘尺寸只有 16/32px，圆角卡片在这个尺度下几乎看不出圆角，
// 所以用小尺寸更清晰的实心圆。放进多尺寸，Windows 会按 DPI 自选。
func buildTrayIcon() []byte {
	sizes := []int{16, 32}

	images := make([][]byte, len(sizes))
	for i, size := range sizes {
		images[i] = renderTrayBitmap(size)
	}

	// --- ICONDIR + ICONDIRENTRY*n + 各自图像 ---
	const headerSize = 6 + 16
	total := 6 + 16*len(sizes)
	entries := make([]byte, 0, 16*len(sizes))
	offset := total
	for i, size := range sizes {
		image := images[i]
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		entries = append(entries,
			dim, dim, // width, height
			0, 0, // color count, reserved
		)
		entries = append(entries, little16(1)...)                  // planes
		entries = append(entries, little16(32)...)                 // bit count
		entries = append(entries, little32(uint32(len(image)))...) // bytes in resource
		entries = append(entries, little32(uint32(offset))...)     // offset
		offset += len(image)
	}

	buf := make([]byte, 0, total+len(images[0])+len(images[1]))
	buf = append(buf, little16(0)...)                  // reserved
	buf = append(buf, little16(1)...)                  // type: icon
	buf = append(buf, little16(uint16(len(sizes)))...) // count
	buf = append(buf, entries...)
	for _, image := range images {
		buf = append(buf, image...)
	}
	return buf
}

// trayLetterMask 是 16x16 的托盘字形位图。
//
// 为什么不用几何渲染：16px 下斜线只有 1~2 像素宽，抗锯齿一糊就成一团。
// 这个尺寸必须像素对齐、手工布点。32px 用同一张图按 2 倍最近邻放大，
// 形状完全一致且边缘锐利。
//
// '#' = 字母（白色），'.' = 圆底（绿色）
var trayLetterMask = [16]string{
	"................",
	"................",
	"................",
	"...##......##...",
	"...##......##...",
	"...###....###...",
	"...##.#..#.##...",
	"...##..##..##...",
	"...##..##..##...",
	"...##......##...",
	"...##......##...",
	"...##......##...",
	"...##......##...",
	"................",
	"................",
	"................",
}

// renderTrayBitmap 渲染单个尺寸的 DIB 数据（BITMAPINFOHEADER + 像素 + AND 掩码）。
func renderTrayBitmap(size int) []byte {
	const dibSize = 40
	pixelBytes := size * size * 4
	maskRow := ((size + 31) / 32) * 4
	maskBytes := maskRow * size
	imageSize := dibSize + pixelBytes + maskBytes

	buf := make([]byte, imageSize)

	// --- BITMAPINFOHEADER ---
	binary.LittleEndian.PutUint32(buf[0:], dibSize)
	binary.LittleEndian.PutUint32(buf[4:], uint32(size))
	binary.LittleEndian.PutUint32(buf[8:], uint32(size*2)) // 高度翻倍（像素 + 掩码）
	binary.LittleEndian.PutUint16(buf[12:], 1)             // planes
	binary.LittleEndian.PutUint16(buf[14:], 32)            // bpp
	binary.LittleEndian.PutUint32(buf[20:], uint32(pixelBytes+maskBytes))

	const (
		bgR, bgG, bgB       = uint8(0x4A), uint8(0xD9), uint8(0x9B) // 与应用同色系
		fgR, fgG, fgB       = uint8(0xFF), uint8(0xFF), uint8(0xFF)
		edgeR, edgeG, edgeB = uint8(0x14), uint8(0x14), uint8(0x14)
	)

	pixels := buf[dibSize:]
	center := float64(size) / 2.0
	radius := float64(size)/2.0 - 1.0
	scale := float64(size) / 16.0

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			row := size - 1 - y // ICO 像素自下而上存储
			off := (row*size + x) * 4

			// 2x2 子采样只为让圆边缘不太毛糙
			inCircle, samples := 0, 0
			for sy := 0; sy < 2; sy++ {
				for sx := 0; sx < 2; sx++ {
					px := float64(x) + 0.25 + float64(sx)*0.5
					py := float64(y) + 0.25 + float64(sy)*0.5
					samples++
					dx, dy := px-center, py-center
					if dx*dx+dy*dy <= radius*radius {
						inCircle++
					}
				}
			}
			if inCircle == 0 {
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = 0, 0, 0, 0
				continue
			}
			// 最外一圈画深色描边：浅色任务栏或花哨壁纸上，
			// 光靠浅绿圆底会糊进背景。
			if float64(inCircle) < float64(samples)*0.5 {
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = edgeB, edgeG, edgeR, 0xFF
				continue
			}

			// 映射回 16x16 位图采样
			mx := int(float64(x) / scale)
			my := int(float64(y) / scale)
			if mx >= 0 && mx < 16 && my >= 0 && my < 16 && trayLetterMask[my][mx] == '#' {
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = fgB, fgG, fgR, 0xFF
			} else {
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = bgB, bgG, bgR, 0xFF
			}
		}
	}

	// AND 掩码全 0：透明度交给 alpha 通道
	return buf
}

func little16(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func little32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}
