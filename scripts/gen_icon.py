#!/usr/bin/env python3
"""从 build/appicon.svg 生成全部图标产物。

唯一源文件是 SVG，其余全部由它派生（不再手绘字形）：

    build/appicon.svg          源（用户给定，512×512）
    build/appicon.png          1024×1024，Wails 构建用
    build/windows/icon.ico     16/24/32/48/64/128/256，exe 资源
    build/tray/tray-16.png     托盘 16px
    build/tray/tray-32.png     托盘 32px（高 DPI）

每个尺寸都让光栅化器按目标尺寸**直接渲染**，而不是从 1024 缩下去：
小尺寸下圆角、笔画、右上角那个白点靠直接渲染才不糊。

用法（必须用 uv 隔离环境，不要把重依赖塞进项目运行时）：

    env -u PYTHONPATH uv run --isolated --with resvg-py python scripts/gen_icon.py

选 resvg-py 的原因：它自带 Rust 二进制，不需要 libcairo。
本机 cairosvg 因缺 libcairo.so.2 直接不可用，rsvg-convert / Chrome 也没装。
"""

from __future__ import annotations

import struct
import sys
import zlib
from pathlib import Path

try:
    import resvg_py
except ImportError:  # pragma: no cover - 只在依赖缺失时触发
    raise SystemExit(
        "缺少 resvg-py。请用：\n"
        "  env -u PYTHONPATH uv run --isolated --with resvg-py python scripts/gen_icon.py"
    )

ROOT = Path(__file__).resolve().parent.parent
SVG_PATH = ROOT / "build" / "appicon.svg"
APP_PNG = ROOT / "build" / "appicon.png"
ICO_PATH = ROOT / "build" / "windows" / "icon.ico"
TRAY_DIR = ROOT / "build" / "tray"

# Wails / Windows 需要的应用图标尺寸。
ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]
# 托盘只用这两个：16 给普通 DPI，32 给 200% 缩放。
TRAY_SIZES = [16, 32]


def render(svg: str, size: int) -> bytes:
    """把 SVG 渲染成 size×size 的 PNG 字节。"""
    return resvg_py.svg_to_bytes(svg_string=svg, width=size, height=size)


def png_dimensions(data: bytes) -> tuple[int, int]:
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise SystemExit("渲染结果不是 PNG")
    width, height = struct.unpack(">II", data[16:24])
    return width, height


def write_ico(path: Path, images: list[tuple[int, bytes]]) -> None:
    """把多张 PNG 打包成 ICO。

    用 PNG 压缩条目（Vista+ 支持），这样 256×256 不会让文件膨胀到几 MB。
    """
    count = len(images)
    directory = b""
    payload = b""
    offset = 6 + 16 * count
    for size, png in images:
        # ICO 里 256 用 0 表示
        dimension = 0 if size >= 256 else size
        directory += struct.pack(
            "<BBBBHHII", dimension, dimension, 0, 0, 1, 32, len(png), offset
        )
        payload += png
        offset += len(png)

    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb") as handle:
        handle.write(struct.pack("<HHH", 0, 1, count))
        handle.write(directory)
        handle.write(payload)


def main() -> int:
    if not SVG_PATH.exists():
        raise SystemExit(f"找不到源文件 {SVG_PATH}")

    svg = SVG_PATH.read_text(encoding="utf-8")

    # 1) 应用主图标 1024
    app_png = render(svg, 1024)
    width, height = png_dimensions(app_png)
    if (width, height) != (1024, 1024):
        raise SystemExit(f"appicon.png 尺寸不对：{width}x{height}")
    APP_PNG.write_bytes(app_png)
    print(f"  build/appicon.png          {width}x{height}  {len(app_png):,} B")

    # 2) exe 资源 ICO：每个尺寸单独渲染
    ico_images = [(size, render(svg, size)) for size in ICO_SIZES]
    for size, png in ico_images:
        w, h = png_dimensions(png)
        if (w, h) != (size, size):
            raise SystemExit(f"ICO {size} 尺寸不对：{w}x{h}")
    write_ico(ICO_PATH, ico_images)
    print(f"  build/windows/icon.ico     {len(ICO_SIZES)} 个尺寸  {ICO_PATH.stat().st_size:,} B")

    # 3) 托盘 PNG：直接按目标尺寸渲染，缩小的图才不糊
    TRAY_DIR.mkdir(parents=True, exist_ok=True)
    for size in TRAY_SIZES:
        png = render(svg, size)
        w, h = png_dimensions(png)
        if (w, h) != (size, size):
            raise SystemExit(f"托盘 {size} 尺寸不对：{w}x{h}")
        target = TRAY_DIR / f"tray-{size}.png"
        target.write_bytes(png)
        print(f"  build/tray/tray-{size}.png   {w}x{h}  {len(png):,} B")

    return 0


if __name__ == "__main__":
    sys.exit(main())
