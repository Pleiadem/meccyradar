package main

import (
	"encoding/binary"
	"testing"
)

// 解析 ICO 目录项，返回 (宽, 高, 位深, 数据偏移, 数据长度)。
type icoEntry struct {
	width, height int
	bpp           int
	offset        int
	size          int
}

func parseICO(t *testing.T, icon []byte) []icoEntry {
	t.Helper()
	if len(icon) < 6 {
		t.Fatalf("ICO 太短：%d 字节", len(icon))
	}
	if got := binary.LittleEndian.Uint16(icon[0:]); got != 0 {
		t.Fatalf("reserved 应为 0，实际 %d", got)
	}
	if got := binary.LittleEndian.Uint16(icon[2:]); got != 1 {
		t.Fatalf("type 应为 1（icon），实际 %d", got)
	}
	count := int(binary.LittleEndian.Uint16(icon[4:]))
	var entries []icoEntry
	for i := 0; i < count; i++ {
		base := 6 + i*16
		w := int(icon[base])
		h := int(icon[base+1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		entries = append(entries, icoEntry{
			width:  w,
			height: h,
			bpp:    int(binary.LittleEndian.Uint16(icon[base+6:])),
			size:   int(binary.LittleEndian.Uint32(icon[base+8:])),
			offset: int(binary.LittleEndian.Uint32(icon[base+12:])),
		})
	}
	return entries
}

func TestBuildTrayIconProducesValidICO(t *testing.T) {
	icon := buildTrayIcon()
	entries := parseICO(t, icon)

	if len(entries) != 2 {
		t.Fatalf("应包含 16px 与 32px 两个尺寸，实际 %d 个", len(entries))
	}
	for i, want := range []int{16, 32} {
		entry := entries[i]
		if entry.width != want || entry.height != want {
			t.Fatalf("第 %d 个尺寸应为 %dx%d，实际 %dx%d", i, want, want, entry.width, entry.height)
		}
		if entry.bpp != 32 {
			t.Fatalf("%dpx 位深应为 32，实际 %d", want, entry.bpp)
		}
		if entry.offset+entry.size > len(icon) {
			t.Fatalf("%dpx 数据越界：offset=%d size=%d total=%d", want, entry.offset, entry.size, len(icon))
		}
		if got := binary.LittleEndian.Uint32(icon[entry.offset:]); got != 40 {
			t.Fatalf("%dpx DIB 头应为 40 字节，实际 %d", want, got)
		}
	}
}

func TestBuildTrayIconHasVisiblePixels(t *testing.T) {
	icon := buildTrayIcon()
	entry := parseICO(t, icon)[0]
	pixels := icon[entry.offset+40:]

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

// 托盘图标必须是字母 M，而不是原来的雷达环（环形 + 圆心）。
func TestTrayIconDrawsLetterM(t *testing.T) {
	icon := buildTrayIcon()
	entry := parseICO(t, icon)[0] // 16px
	pixels := icon[entry.offset+40:]

	pixelAt := func(x, y int) (r, g, b, a byte) {
		row := entry.height - 1 - y // ICO 像素自下而上存储
		off := (row*entry.width + x) * 4
		return pixels[off+2], pixels[off+1], pixels[off], pixels[off+3]
	}
	isWhite := func(x, y int) bool {
		r, g, b, a := pixelAt(x, y)
		return a > 0 && r > 200 && g > 200 && b > 200
	}

	// M 的两条竖笔：中间高度上，左右两半各应有白色像素
	middleY := entry.height / 2
	leftWhite, rightWhite := false, false
	for x := 0; x < entry.width/2; x++ {
		if isWhite(x, middleY) {
			leftWhite = true
		}
	}
	for x := entry.width / 2; x < entry.width; x++ {
		if isWhite(x, middleY) {
			rightWhite = true
		}
	}
	if !leftWhite || !rightWhite {
		t.Fatalf("M 应在左右两侧都有白色笔画（left=%v right=%v）", leftWhite, rightWhite)
	}

	// 圆底之外的四角必须透明
	for _, corner := range [][2]int{{0, 0}, {entry.width - 1, 0}, {0, entry.height - 1}, {entry.width - 1, entry.height - 1}} {
		if _, _, _, a := pixelAt(corner[0], corner[1]); a != 0 {
			t.Fatalf("圆底之外应透明，角 %v 不透明", corner)
		}
	}

	// 区分"字母 M"与"旧的雷达环+圆点"：
	// 旧图形是一个细圆环 + 中心一个小圆点，环与圆心之间是空白；
	// M 则是三条粗笔画填满圆内大部分区域。
	// 所以统计白色占比最稳：M 的白色远多于旧图形的细环+小点。
	white := 0
	inside := 0
	for y := 0; y < entry.height; y++ {
		for x := 0; x < entry.width; x++ {
			if _, _, _, a := pixelAt(x, y); a == 0 {
				continue
			}
			inside++
			if isWhite(x, y) {
				white++
			}
		}
	}
	if inside == 0 {
		t.Fatal("圆底内没有像素")
	}
	// 旧雷达图标用的是绿色环 + 绿色圆心，一个白色像素都没有，
	// 所以 0.15 已经足以区分；阈值不该定得刚好卡住当前设计。
	ratio := float64(white) / float64(inside)
	if ratio < 0.15 {
		t.Fatalf("白色笔画占比过低（%.2f），图标可能仍是旧的雷达环", ratio)
	}

	// 再补一层：M 的笔画要纵向铺开（旧图形只有一条细环 + 中心小点，
	// 含白色的行数会很少）。
	rowsWithWhite := 0
	for y := 0; y < entry.height; y++ {
		for x := 0; x < entry.width; x++ {
			if isWhite(x, y) {
				rowsWithWhite++
				break
			}
		}
	}
	if rowsWithWhite < 8 {
		t.Fatalf("含白色笔画的只有 %d 行，M 应纵向铺开至少 8 行", rowsWithWhite)
	}
}
