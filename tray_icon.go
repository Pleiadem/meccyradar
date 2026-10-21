package main

import "encoding/binary"

// buildTrayIcon 生成一个 16x16、32 位色的 ICO 图标。
//
// 为什么自己拼字节：systray 在 Windows 上要求 ICO 格式，而项目里只有
// appicon.png；本地没有图像库可依赖，为了一个 16x16 的小图标引入图片
// 处理链路不值得。这里直接按 ICO 规范写头 + 32bpp 位图 + AND 掩码。
//
// 结构：
//
//	ICONDIR        6 字节
//	ICONDIRENTRY  16 字节
//	BITMAPINFO    40 字节
//	像素数据      ize*size*4（BGRA，自下而上）
//	AND 掩码      每行 4 字节对齐，全 0 表示不透明
func buildTrayIcon() []byte {
	const size = 16

	const (
		headerSize = 6 + 16
		dibSize    = 40
		pixelBytes = size * size * 4
		maskRow    = 4 // 16 位掩码 → 2 字节，补齐到 4 字节
		maskBytes  = maskRow * size
		imageSize  = dibSize + pixelBytes + maskBytes
	)

	buf := make([]byte, headerSize+imageSize)

	// --- ICONDIR ---
	binary.LittleEndian.PutUint16(buf[0:], 0) // reserved
	binary.LittleEndian.PutUint16(buf[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(buf[4:], 1) // count

	// --- ICONDIRENTRY ---
	buf[6] = size                                              // width
	buf[7] = size                                              // height
	buf[8] = 0                                                 // color count
	buf[9] = 0                                                 // reserved
	binary.LittleEndian.PutUint16(buf[10:], 1)                 // planes
	binary.LittleEndian.PutUint16(buf[12:], 32)                // bit count
	binary.LittleEndian.PutUint32(buf[14:], uint32(imageSize)) // bytes in resource
	binary.LittleEndian.PutUint32(buf[18:], headerSize)        // offset

	// --- BITMAPINFOHEADER ---
	dib := buf[headerSize:]
	binary.LittleEndian.PutUint32(dib[0:], dibSize)
	binary.LittleEndian.PutUint32(dib[4:], size)
	binary.LittleEndian.PutUint32(dib[8:], size*2) // 高度翻倍（像素 + 掩码）
	binary.LittleEndian.PutUint16(dib[12:], 1)     // planes
	binary.LittleEndian.PutUint16(dib[14:], 32)    // bpp
	binary.LittleEndian.PutUint32(dib[20:], uint32(pixelBytes+maskBytes))

	// --- 像素：一个环形 + 圆心，形似雷达 ---
	pixels := dib[dibSize:]
	center := float64(size-1) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// ICO 像素是自下而上存的
			row := size - 1 - y
			off := (row*size + x) * 4

			dx := float64(x) - center
			dy := float64(y) - center
			dist := dx*dx + dy*dy

			const accentR, accentG, accentB = uint8(0x4A), uint8(0xD9), uint8(0x9B)
			switch {
			case dist <= 2.6: // 圆心
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = accentB, accentG, accentR, 0xFF
			case dist >= 20.0 && dist <= 34.0: // 外环
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = accentB, accentG, accentR, 0xFF
			default:
				pixels[off], pixels[off+1], pixels[off+2], pixels[off+3] = 0, 0, 0, 0
			}
		}
	}

	// AND 掩码全 0：透明度交给 alpha 通道
	return buf
}
