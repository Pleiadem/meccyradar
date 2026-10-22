package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

// 托盘图标必须来自 build/appicon.svg 的派生产物，而不是代码手绘。
//
// 历史教训：早期托盘是代码画的绿环，后来一度手绘字母 M，两版都跟
// 任务栏/窗口图标对不上，用户看到的是两个不同的品牌形象。
// 现在 SVG 是唯一源，这里钉住"托盘用的就是那份渲染结果"。

func TestTrayIconEmbedsSVGRenders(t *testing.T) {
	if len(trayPNG16) == 0 || len(trayPNG32) == 0 {
		t.Fatal("托盘 PNG 没有嵌入：build/tray/tray-{16,32}.png 缺失，先跑 scripts/gen_icon.py")
	}
	for name, data := range map[string][]byte{"tray-16": trayPNG16, "tray-32": trayPNG32} {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s 不是合法 PNG：%v", name, err)
		}
		want := 16
		if name == "tray-32" {
			want = 32
		}
		if b := img.Bounds(); b.Dx() != want || b.Dy() != want {
			t.Fatalf("%s 尺寸应为 %d×%d，实际 %d×%d", name, want, want, b.Dx(), b.Dy())
		}
	}
}

// 托盘 ICO 必须是合法的多尺寸 ICO，且尺寸表正确。
func TestBuildTrayIconProducesValidICO(t *testing.T) {
	data := buildTrayIcon()
	if len(data) == 0 {
		t.Fatal("托盘 ICO 为空")
	}
	if got := binary.LittleEndian.Uint16(data[0:]); got != 0 {
		t.Fatalf("ICONDIR.reserved 应为 0，实际 %d", got)
	}
	if got := binary.LittleEndian.Uint16(data[2:]); got != 1 {
		t.Fatalf("ICONDIR.type 应为 1（icon），实际 %d", got)
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	if count != len(traySizes) {
		t.Fatalf("应包含 %d 个尺寸，实际 %d", len(traySizes), count)
	}

	for i := 0; i < count; i++ {
		entry := 6 + 16*i
		width := int(data[entry])
		bpp := int(binary.LittleEndian.Uint16(data[entry+6:]))
		size := int(binary.LittleEndian.Uint32(data[entry+8:]))
		offset := int(binary.LittleEndian.Uint32(data[entry+12:]))

		if width != traySizes[i] {
			t.Fatalf("第 %d 个尺寸应为 %d，实际 %d", i, traySizes[i], width)
		}
		if bpp != 32 {
			t.Fatalf("第 %d 个应为 32bpp，实际 %d", i, bpp)
		}
		if offset+size > len(data) {
			t.Fatalf("第 %d 个数据越界：offset=%d size=%d total=%d", i, offset, size, len(data))
		}
		// DIB 头自校验
		if got := int(binary.LittleEndian.Uint32(data[offset:])); got != 40 {
			t.Fatalf("第 %d 个 BITMAPINFOHEADER 大小应为 40，实际 %d", i, got)
		}
	}
}

// 图标必须有可见像素：全透明的话任务栏里什么都看不到。
func TestTrayIconHasVisiblePixels(t *testing.T) {
	data := buildTrayIcon()
	count := int(binary.LittleEndian.Uint16(data[4:]))
	for i := 0; i < count; i++ {
		entry := 6 + 16*i
		size := int(data[entry])
		offset := int(binary.LittleEndian.Uint32(data[entry+12:]))
		pixels := data[offset+40 : offset+40+size*size*4]

		opaque := 0
		for p := 0; p < len(pixels); p += 4 {
			if pixels[p+3] > 128 {
				opaque++
			}
		}
		if opaque == 0 {
			t.Fatalf("%dpx 图标全透明，任务栏里会看不见", size)
		}
		// 圆角矩形应该占满大部分面积，不至于只剩一个小点
		if ratio := float64(opaque) / float64(size*size); ratio < 0.5 {
			t.Fatalf("%dpx 图标不透明像素只占 %.0f%%，太小了", size, ratio*100)
		}
	}
}

// 托盘图标应当与源 SVG 的渐变一致（左上偏蓝紫、右下偏绿），
// 用来防止误把旧的手绘图形塞回来。
func TestTrayIconMatchesSVGGradient(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(trayPNG16))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()

	topLeft := img.At(b.Min.X+2, b.Min.Y+2)
	r0, g0, bl0, _ := topLeft.RGBA()
	// 左上角：蓝紫，蓝色分量应明显高于绿色
	if !(bl0>>8 > g0>>8) {
		t.Fatalf("左上角应是偏蓝紫的渐变起点，实际 r=%d g=%d b=%d", r0>>8, g0>>8, bl0>>8)
	}

	bottomRight := img.At(b.Max.X-3, b.Max.Y-3)
	r1, g1, bl1, _ := bottomRight.RGBA()
	// 右下角：浅绿，绿色分量应不低于蓝色
	if g1>>8 < bl1>>8 {
		t.Fatalf("右下角应是偏绿的渐变终点，实际 r=%d g=%d b=%d", r1>>8, g1>>8, bl1>>8)
	}
}
