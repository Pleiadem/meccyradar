#!/usr/bin/env python3
"""生成 MeccyRadar 应用图标：在**原 W 图标**上只替换字母为 M。

设计约束（来自需求）：保持原设计风格，只改字母，其他元素完全不变。

因此本脚本不做"重新设计"，而是：
  1. 读入原 W 图标（build/appicon-w-original.png，取自改动前的版本）
  2. 只在字母包围盒内把 W 的墨迹抹成卡片底色
  3. 在同一区域按原字形的度量画 M

原图标实测参数（像素扫描得到，不是目测）：
  画布        1024x1024
  背景        全透明 (0,0,0,0)
  卡片        #FFFFFF 纯白，x[109,925] y[133,908] → 817x776，圆角半径约 58
  字母        #1E1E1E，包围盒 584x534
  笔画宽度    中部水平扫描得 126 / 89 / 89 / 126
              → 外侧竖笔 126px，内侧斜笔 89px

M 的字形按同一套度量构造：两条 126px 竖笔 + 两条 89px 斜笔构成中央 V，
V 顶点平切（与原 W 中间峰顶平切一致）。

不依赖 Pillow / ImageMagick，只用标准库。
"""

import struct
import sys
import zlib

# ---------------------------------------------------------------- PNG 读写

def load_png(path):
    data = open(path, "rb").read()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise SystemExit(f"{path} 不是 PNG")
    i, width, height, idat, colortype = 8, None, None, b"", None
    while i < len(data):
        length = struct.unpack(">I", data[i:i + 4])[0]
        tag = data[i + 4:i + 8]
        chunk = data[i + 8:i + 8 + length]
        if tag == b"IHDR":
            width, height, _bitdepth, colortype = struct.unpack(">IIBB", chunk[:10])
        elif tag == b"IDAT":
            idat += chunk
        elif tag == b"IEND":
            break
        i += 12 + length
    channels = {0: 1, 2: 3, 4: 2, 6: 4}[colortype]
    raw = zlib.decompress(idat)
    stride = width * channels
    out = bytearray()
    prev = bytearray(stride)
    pos = 0
    for _ in range(height):
        filter_type = raw[pos]
        pos += 1
        line = bytearray(raw[pos:pos + stride])
        pos += stride
        for x in range(stride):
            a = line[x - channels] if x >= channels else 0
            b = prev[x]
            c = prev[x - channels] if x >= channels else 0
            if filter_type == 1:
                line[x] = (line[x] + a) & 0xFF
            elif filter_type == 2:
                line[x] = (line[x] + b) & 0xFF
            elif filter_type == 3:
                line[x] = (line[x] + (a + b) // 2) & 0xFF
            elif filter_type == 4:
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                predictor = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                line[x] = (line[x] + predictor) & 0xFF
        out += line
        prev = line
    if channels == 4:
        return width, height, bytes(out)
    rgba = bytearray()
    for idx in range(0, len(out), channels):
        if channels == 3:
            rgba += out[idx:idx + 3] + b"\xff"
        elif channels == 1:
            rgba += bytes((out[idx],) * 3) + b"\xff"
        else:
            rgba += bytes((out[idx],) * 3) + bytes((out[idx + 1],))
    return width, height, bytes(rgba)


def write_png(path, width, height, rgba):
    stride = width * 4
    raw = b"".join(b"\x00" + rgba[y * stride:(y + 1) * stride] for y in range(height))

    def chunk(tag, payload):
        body = tag + payload
        return struct.pack(">I", len(payload)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    header = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    with open(path, "wb") as handle:
        handle.write(b"\x89PNG\r\n\x1a\n")
        handle.write(chunk(b"IHDR", header))
        handle.write(chunk(b"IDAT", zlib.compress(raw, 9)))
        handle.write(chunk(b"IEND", b""))


# ---------------------------------------------------------------- 字形

CARD_COLOR = (0xFF, 0xFF, 0xFF)
LETTER_COLOR = (0x1E, 0x1E, 0x1E)

LETTER_WIDTH = 584
LETTER_HEIGHT = 534
STEM_WIDTH = 126      # 外侧竖笔
DIAG_WIDTH = 89       # 内侧斜笔


def m_polygons(left, top):
    """在给定左上角构造 M 的四个多边形（两条竖笔 + 两条斜笔）。"""
    w, h = LETTER_WIDTH, LETTER_HEIGHT
    stem, diag = STEM_WIDTH, DIAG_WIDTH
    center_x = left + w / 2.0
    right = left + w
    bottom = top + h

    left_stem = [(left, top), (left + stem, top), (left + stem, bottom), (left, bottom)]
    right_stem = [(right - stem, top), (right, top), (right, bottom), (right - stem, bottom)]

    # 斜笔向顶点收分：顶部保持 89px，落到底部收窄到 60px。
    # 不收分的话两条 89px 斜笔会重叠出近 200px 高的实心块，
    # V 看起来像被截断的梯形而不是一个尖。
    apex_half = 30.0
    left_diag = [
        (left + stem, top),
        (left + stem + diag, top),
        (center_x + apex_half, bottom),
        (center_x - apex_half, bottom),
    ]
    right_diag = [
        (right - stem - diag, top),
        (right - stem, top),
        (center_x + apex_half, bottom),
        (center_x - apex_half, bottom),
    ]
    return [left_stem, right_stem, left_diag, right_diag]


def point_in_polygon(x, y, points):
    inside = False
    count = len(points)
    for i in range(count):
        x1, y1 = points[i]
        x2, y2 = points[(i + 1) % count]
        if (y1 > y) != (y2 > y):
            xin = x1 + (y - y1) * (x2 - x1) / (y2 - y1)
            if x < xin:
                inside = not inside
    return inside


def draw_letter(rgba, width, height, polygons, samples=4):
    """把多边形填充进 RGBA 缓冲，带超采样抗锯齿。"""
    xs = [p[0] for poly in polygons for p in poly]
    ys = [p[1] for poly in polygons for p in poly]
    x0, x1 = max(0, int(min(xs)) - 1), min(width - 1, int(max(xs)) + 2)
    y0, y1 = max(0, int(min(ys)) - 1), min(height - 1, int(max(ys)) + 2)

    step = 1.0 / samples
    total = samples * samples
    for y in range(y0, y1 + 1):
        for x in range(x0, x1 + 1):
            hits = 0
            for sy in range(samples):
                for sx in range(samples):
                    px = x + (sx + 0.5) * step
                    py = y + (sy + 0.5) * step
                    if any(point_in_polygon(px, py, poly) for poly in polygons):
                        hits += 1
            if not hits:
                continue
            alpha = hits / total
            offset = (y * width + x) * 4
            for c in range(3):
                base = rgba[offset + c]
                rgba[offset + c] = int(base * (1 - alpha) + LETTER_COLOR[c] * alpha + 0.5)


def fill_rect(rgba, width, height, x0, y0, x1, y1, color):
    for y in range(max(0, y0), min(y1 + 1, height)):
        row = y * width * 4
        for x in range(max(0, x0), min(x1 + 1, width)):
            offset = row + x * 4
            rgba[offset] = color[0]
            rgba[offset + 1] = color[1]
            rgba[offset + 2] = color[2]
            rgba[offset + 3] = 255


# ---------------------------------------------------------------- 缩放 / ICO

def downscale(rgba, width, height, target):
    """盒式滤波缩放。"""
    if target == width:
        return bytes(rgba)
    out = bytearray(target * target * 4)
    for ty in range(target):
        y0 = ty * height // target
        y1 = max(y0 + 1, (ty + 1) * height // target)
        for tx in range(target):
            x0 = tx * width // target
            x1 = max(x0 + 1, (tx + 1) * width // target)
            acc = [0, 0, 0, 0]
            count = 0
            for sy in range(y0, y1):
                base = sy * width * 4
                for sx in range(x0, x1):
                    offset = base + sx * 4
                    acc[0] += rgba[offset]
                    acc[1] += rgba[offset + 1]
                    acc[2] += rgba[offset + 2]
                    acc[3] += rgba[offset + 3]
                    count += 1
            dest = (ty * target + tx) * 4
            for c in range(4):
                out[dest + c] = acc[c] // count
    return bytes(out)


def write_ico(path, rgba, width, sizes):
    images = []
    for size in sizes:
        scaled = downscale(rgba, width, width, size)
        stride = size * 4
        raw = b"".join(b"\x00" + scaled[y * stride:(y + 1) * stride] for y in range(size))

        def chunk(tag, payload):
            body = tag + payload
            return struct.pack(">I", len(payload)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

        header = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
        png = (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header)
               + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b""))
        images.append((size, png))

    count = len(images)
    directory = b""
    payload = b""
    offset = 6 + 16 * count
    for size, png in images:
        dim = 0 if size >= 256 else size
        directory += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(png), offset)
        payload += png
        offset += len(png)

    with open(path, "wb") as handle:
        handle.write(struct.pack("<HHH", 0, 1, count))
        handle.write(directory)
        handle.write(payload)


# ---------------------------------------------------------------- 主流程

def find_letter_bbox(rgba, width, height, threshold=80):
    minx, miny, maxx, maxy = width, height, -1, -1
    for y in range(height):
        base = y * width * 4
        for x in range(width):
            offset = base + x * 4
            if rgba[offset + 3] > 128 and rgba[offset] < threshold:
                minx = min(minx, x)
                maxx = max(maxx, x)
                miny = min(miny, y)
                maxy = max(maxy, y)
    if maxx < 0:
        raise SystemExit("原图标里找不到字母像素")
    return minx, miny, maxx, maxy


def main():
    source = sys.argv[1] if len(sys.argv) > 1 else "build/appicon-w-original.png"
    out_png = sys.argv[2] if len(sys.argv) > 2 else "build/appicon.png"
    out_ico = sys.argv[3] if len(sys.argv) > 3 else "build/windows/icon.ico"

    width, height, rgba = load_png(source)
    rgba = bytearray(rgba)
    wx0, wy0, wx1, wy1 = find_letter_bbox(rgba, width, height)
    print(f"原字母包围盒: x[{wx0},{wx1}] y[{wy0},{wy1}]  {wx1 - wx0 + 1}x{wy1 - wy0 + 1}")

    # 卡片中心：由卡片不透明区域包围盒得出（x[109,925] y[133,908]）
    cx, cy = 517.0, 520.5
    left = cx - LETTER_WIDTH / 2.0
    top = cy - LETTER_HEIGHT / 2.0

    # 1) 抹掉原字母。范围取"原包围盒 ∪ 新字形包围盒"再留 4px 余量，
    #    确保连抗锯齿边缘一起清掉。
    erase_x0 = int(min(wx0, left)) - 4
    erase_y0 = int(min(wy0, top)) - 4
    erase_x1 = int(max(wx1, left + LETTER_WIDTH)) + 4
    erase_y1 = int(max(wy1, top + LETTER_HEIGHT)) + 4
    fill_rect(rgba, width, height, erase_x0, erase_y0, erase_x1, erase_y1, CARD_COLOR)

    # 2) 画 M
    draw_letter(rgba, width, height, m_polygons(left, top))

    write_png(out_png, width, height, bytes(rgba))
    write_ico(out_ico, bytes(rgba), width, [16, 24, 32, 48, 64, 128, 256])
    print(f"已生成 {out_png} 与 {out_ico}")


if __name__ == "__main__":
    main()
