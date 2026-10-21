#!/usr/bin/env python3
"""生成 MeccyRadar 图标：黑色底 + 白色圆角卡片 + 深炭灰粗体字母。

保持与现有 W 图标完全一致的设计规范（实测自 build/appicon.png）：
  画布 1024x1024，外底 #000000，白色圆角卡片约 804x764、圆角 80，
  字母 #2D2D2D、极粗几何无衬线、平切笔画端。

不依赖 Pillow / ImageMagick：只用标准库手写像素与 PNG 编码。
字母用多边形填充实现，笔画厚度与平切端可直接控制。
"""

import math
import struct
import sys
import zlib

CANVAS = 1024
BG = (0x00, 0x00, 0x00)
CARD = (0xFF, 0xFF, 0xFF)
LETTER = (0x2D, 0x2D, 0x2D)

CARD_W = 804
CARD_H = 764
CARD_R = 80


def _rounded_rect_mask(size, width, height, radius):
    """返回圆角矩形覆盖判断函数（内部坐标）。"""
    left = (size - width) / 2.0
    top = (size - height) / 2.0
    right = left + width
    bottom = top + height

    def inside(x, y):
        if x < left or x > right or y < top or y > bottom:
            return False
        # 四个角做圆角裁剪
        cx = left + radius if x < left + radius else (right - radius if x > right - radius else x)
        cy = top + radius if y < top + radius else (bottom - radius if y > bottom - radius else y)
        dx, dy = x - cx, y - cy
        if dx == 0 and dy == 0:
            return True
        return dx * dx + dy * dy <= radius * radius

    return inside


def _point_in_polygon(x, y, points):
    """射线法判断点是否在多边形内。"""
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


def _letter_m_polygons(center_x, center_y, height, width, stroke):
    """构造粗体几何 M 的多边形集合。

    M 由三段组成，全部平切端：
      左竖笔、右竖笔、中间的 V 形斜笔。
    这样能保证与 W 图标同样厚重的视觉重量，且笔画端是平的。
    """
    top = center_y - height / 2.0
    bottom = center_y + height / 2.0
    left = center_x - width / 2.0
    right = center_x + width / 2.0

    # 左竖笔
    left_stem = [
        (left, bottom),
        (left + stroke, bottom),
        (left + stroke, top),
        (left, top),
    ]
    # 右竖笔
    right_stem = [
        (right - stroke, bottom),
        (right, bottom),
        (right, top),
        (right - stroke, top),
    ]

    # 中间 V：用一整块闭合多边形，而不是两条各自独立的多边形。
    # 分成两块时，尖点处两块只在一条边上相接，16x16 缩放后会在中心
    # 留下一个孤立暗点（实测确认）。整块闭合就没有这个缝。
    apex_y = bottom - height * 0.18  # 尖点略高于底边，避免与竖笔底部齐平
    outer_left = left + stroke * 0.55
    outer_right = right - stroke * 0.55
    mid = center_x
    half_tip = stroke * 0.5

    v_block = [
        (outer_left, top),                       # 左上外角
        (outer_left + stroke, top),              # 左上内角
        (mid - half_tip, apex_y),                # 下行到尖点左侧
        (mid + half_tip, apex_y),                # 尖点右侧（底边）
        (outer_right, top),                      # 右上内角
        (outer_right - stroke, top),             # 右上外角
        (mid + half_tip, apex_y - stroke),       # 回程：尖点右侧偏上
        (mid - half_tip, apex_y - stroke),       # 回程：尖点左侧偏上
    ]
    return [left_stem, right_stem, v_block]


def render(size=CANVAS, samples=4):
    """渲染成 RGBA 像素缓冲，按行优先自上而下。

    每个像素取 samples x samples 个子样本求平均（超采样）。
    不做的话斜线是硬台阶，128px 以上放大能明显看到锯齿。
    小尺寸（16/32）本来就要缩小，超采样同样能让边缘更干净。
    """
    card_inside = _rounded_rect_mask(size, CARD_W * size / CANVAS, CARD_H * size / CANVAS, CARD_R * size / CANVAS)
    scale = size / CANVAS
    polygons = _letter_m_polygons(
        center_x=size / 2.0,
        center_y=size / 2.0,
        height=536 * scale,
        width=584 * scale,
        stroke=110 * scale,
    )

    step = 1.0 / samples
    total = samples * samples
    rows = []
    for y in range(size):
        row = bytearray()
        for x in range(size):
            acc_bg = acc_card = acc_letter = 0
            for sy in range(samples):
                for sx in range(samples):
                    px = x + (sx + 0.5) * step
                    py = y + (sy + 0.5) * step
                    if not card_inside(px, py):
                        acc_bg += 1
                    elif any(_point_in_polygon(px, py, poly) for poly in polygons):
                        acc_letter += 1
                    else:
                        acc_card += 1
            r = (BG[0] * acc_bg + CARD[0] * acc_card + LETTER[0] * acc_letter) / total
            g = (BG[1] * acc_bg + CARD[1] * acc_card + LETTER[1] * acc_letter) / total
            b = (BG[2] * acc_bg + CARD[2] * acc_card + LETTER[2] * acc_letter) / total
            row += bytes((int(r + 0.5), int(g + 0.5), int(b + 0.5), 255))
        rows.append(bytes(row))
    return rows


def write_png(path, size, rows):
    raw = b"".join(b"\x00" + row for row in rows)

    def chunk(tag, data):
        body = tag + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    header = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
    with open(path, "wb") as handle:
        handle.write(b"\x89PNG\r\n\x1a\n")
        handle.write(chunk(b"IHDR", header))
        handle.write(chunk(b"IDAT", zlib.compress(raw, 9)))
        handle.write(chunk(b"IEND", b""))


def write_ico(path, sizes):
    """把多个尺寸的 PNG 打成一个 ICO（Vista+ 支持 PNG 压缩的 ICO）。"""
    images = []
    for size in sizes:
        rows = render(size)
        raw = b"".join(b"\x00" + row for row in rows)

        def chunk(tag, data):
            body = tag + data
            return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

        header = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
        png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")
        images.append((size, png))

    count = len(images)
    directory = b""
    offset = 6 + 16 * count
    payload = b""
    for size, png in images:
        dim = 0 if size >= 256 else size
        directory += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(png), offset)
        payload += png
        offset += len(png)

    with open(path, "wb") as handle:
        handle.write(struct.pack("<HHH", 0, 1, count))
        handle.write(directory)
        handle.write(payload)


if __name__ == "__main__":
    target = sys.argv[1] if len(sys.argv) > 1 else "."
    write_png(f"{target}/appicon.png", CANVAS, render(CANVAS))
    write_ico(f"{target}/icon.ico", [16, 24, 32, 48, 64, 128, 256])
    print("已生成 appicon.png 与 icon.ico")
