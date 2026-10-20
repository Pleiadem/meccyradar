package main

import (
	"encoding/binary"
	"testing"
)

func TestBuildTrayIconProducesValidICO(t *testing.T) {
	icon := buildTrayIcon()

	// ICONDIR
	if got := binary.LittleEndian.Uint16(icon[0:]); got != 0 {
		t.Fatalf("reserved 应为 0，实际 %d", got)
	}
	if got := binary.LittleEndian.Uint16(icon[2:]); got != 1 {
		t.Fatalf("type 应为 1（icon），实际 %d", got)
	}
	if got := binary.LittleEndian.Uint16(icon[4:]); got != 1 {
		t.Fatalf("count 应为 1，实际 %d", got)
	}

	// ICONDIRENTRY
	if icon[6] != 16 || icon[7] != 16 {
		t.Fatalf("应为 16x16，实际 %dx%d", icon[6], icon[7])
	}
	if got := binary.LittleEndian.Uint16(icon[12:]); got != 32 {
		t.Fatalf("位深应为 32，实际 %d", got)
	}

	size := binary.LittleEndian.Uint32(icon[14:])
	offset := binary.LittleEndian.Uint32(icon[18:])
	if offset != 22 {
		t.Fatalf("图像数据应紧跟在 22 字节头之后，实际 %d", offset)
	}
	if int(offset)+int(size) != len(icon) {
		t.Fatalf("声明的数据长度与文件长度不符：offset %d + size %d != %d", offset, size, len(icon))
	}

	// BITMAPINFOHEADER
	dib := icon[offset:]
	if got := binary.LittleEndian.Uint32(dib[0:]); got != 40 {
		t.Fatalf("DIB 头长度应为 40，实际 %d", got)
	}
	if got := binary.LittleEndian.Uint32(dib[4:]); got != 16 {
		t.Fatalf("DIB 宽度应为 16，实际 %d", got)
	}
	if got := binary.LittleEndian.Uint32(dib[8:]); got != 32 {
		t.Fatalf("DIB 高度应为 32（像素+掩码），实际 %d", got)
	}
}

func TestBuildTrayIconHasVisiblePixels(t *testing.T) {
	icon := buildTrayIcon()
	dib := icon[22:]
	pixels := dib[40:]

	opaque := 0
	for i := 3; i < len(pixels); i += 4 {
		if pixels[i] > 0 {
			opaque++
		}
	}
	if opaque == 0 {
		t.Fatal("图标全透明，托盘上看不到")
	}
}
