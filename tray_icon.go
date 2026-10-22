package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
)

// 托盘图标来自 build/appicon.svg 的派生产物，不再用代码手绘。
//
// 早期版本在代码里画"雷达环"（绿环 + 圆心），后来一度改成手绘字母 M，
// 两版都跟应用图标对不上——托盘和任务栏显示的是两个不同的品牌形象。
// 现在统一：SVG 是唯一源，scripts/gen_icon.py 按目标尺寸直接渲染出
// build/tray/tray-16.png 与 tray-32.png，这里只负责把它们包成 ICO。
//
// 为什么按目标尺寸单独渲染而不是从 1024 缩下来：16px 下圆角、笔画和
// 右上角那个白点靠缩小会糊成一团，直接渲染才干净。

//go:embed build/tray/tray-16.png
var trayPNG16 []byte

//go:embed build/tray/tray-32.png
var trayPNG32 []byte

// traySizes 是托盘要提供的尺寸：16 给普通 DPI，32 给 200% 缩放。
var traySizes = []int{16, 32}

// buildTrayIcon 生成托盘用的 ICO（16 与 32 两个尺寸）。
//
// systray 在 Windows 上要 ICO。这里用 32bpp DIB 条目而不是 PNG 压缩条目：
// CreateIconFromResourceEx 对 PNG-in-ICO 的支持依赖系统版本，
// 而 16/32 这么小的图用 DIB 也只有 1~4 KB，没必要冒兼容风险。
func buildTrayIcon() []byte {
	images := make([][]byte, 0, len(traySizes))
	for _, size := range traySizes {
		source := trayPNG16
		if size == 32 {
			source = trayPNG32
		}
		img, err := png.Decode(bytes.NewReader(source))
		if err != nil {
			// 嵌入的资源是构建期产物，解不开说明生成脚本出问题了。
			// 返回 nil 让 systray 用默认图标，总比 panic 掉整个应用好。
			return nil
		}
		images = append(images, encodeICODIB(img, size))
	}
	return packICO(images)
}

// encodeICODIB 把一张图片编码成 ICO 的 BITMAPINFOHEADER + BGRA 像素 + AND 掩码。
func encodeICODIB(img image.Image, size int) []byte {
	const dibSize = 40
	pixelBytes := size * size * 4
	// AND 掩码每行按 4 字节对齐：16px → 2 字节 → 补到 4；32px → 4 字节。
	maskRow := ((size + 31) / 32) * 4
	maskBytes := maskRow * size

	buf := make([]byte, dibSize+pixelBytes+maskBytes)

	binary.LittleEndian.PutUint32(buf[0:], dibSize)
	binary.LittleEndian.PutUint32(buf[4:], uint32(size))
	binary.LittleEndian.PutUint32(buf[8:], uint32(size*2)) // 高度翻倍（像素 + 掩码）
	binary.LittleEndian.PutUint16(buf[12:], 1)             // planes
	binary.LittleEndian.PutUint16(buf[14:], 32)            // bpp
	binary.LittleEndian.PutUint32(buf[20:], uint32(pixelBytes+maskBytes))

	pixels := buf[dibSize : dibSize+pixelBytes]
	bounds := img.Bounds()
	for y := 0; y < size; y++ {
		// ICO 像素自下而上存储
		row := size - 1 - y
		for x := 0; x < size; x++ {
			// 源图可能不是正好 size×size（理论上不会），做一次夹取
			sx := bounds.Min.X + x*size/bounds.Dx()
			sy := bounds.Min.Y + y*size/bounds.Dy()
			r, g, b, a := img.At(sx, sy).RGBA()
			off := (row*size + x) * 4
			// RGBA() 返回的是 16 位预乘值，取高 8 位即可
			pixels[off] = byte(b >> 8)
			pixels[off+1] = byte(g >> 8)
			pixels[off+2] = byte(r >> 8)
			pixels[off+3] = byte(a >> 8)
		}
	}

	// AND 掩码全 0：透明度交给 alpha 通道，掩码只负责形状裁切
	return buf
}

// packICO 把若干 ICONIMAGE 拼成完整的 ICO 文件。
func packICO(images [][]byte) []byte {
	if len(images) == 0 {
		return nil
	}
	headerSize := 6 + 16*len(images)
	total := headerSize
	for _, data := range images {
		total += len(data)
	}

	buf := make([]byte, headerSize, total)
	binary.LittleEndian.PutUint16(buf[0:], 0) // reserved
	binary.LittleEndian.PutUint16(buf[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(images)))

	offset := headerSize
	for index, data := range images {
		// 尺寸从 DIB 头里读，避免再传一遍参数
		size := int(binary.LittleEndian.Uint32(data[4:]))
		entry := 6 + 16*index
		buf[entry] = byte(size % 256) // 256 记作 0
		buf[entry+1] = byte(size % 256)
		buf[entry+2] = 0 // color count
		buf[entry+3] = 0 // reserved
		binary.LittleEndian.PutUint16(buf[entry+4:], 1)                    // planes
		binary.LittleEndian.PutUint16(buf[entry+6:], 32)                   // bpp
		binary.LittleEndian.PutUint32(buf[entry+8:], uint32(len(data)))    // bytes
		binary.LittleEndian.PutUint32(buf[entry+12:], uint32(offset))      // offset
		buf = append(buf, data...)
		offset += len(data)
	}
	return buf
}

// errTrayIconUnavailable 供测试断言"资源缺失时不会 panic"。
var errTrayIconUnavailable = errors.New("托盘图标资源不可用")
