#!/usr/bin/env python3
"""Find the sprites on a raw art sheet: background from the border, foreground mask, connected blobs.

    python3 tools/art/segment.py <sheet> <out.png> [--tol 40] [--pool 4] [--min 60]

Writes an annotated copy with numbered boxes and prints `index x0 y0 x1 y1 area` per blob (sheet pixels).
"""
import sys
from collections import deque

import numpy as np
from PIL import Image, ImageDraw


def background(rgb):
    border = np.concatenate([rgb[0], rgb[-1], rgb[:, 0], rgb[:, -1]])
    return np.median(border, axis=0)


def foreground(rgb, tol):
    bg = background(rgb)
    return np.sqrt(((rgb.astype(np.float32) - bg) ** 2).sum(axis=2)) > tol


def blobs(mask, pool, min_cells):
    h, w = mask.shape
    small = mask[: h - h % pool, : w - w % pool].reshape(h // pool, pool, w // pool, pool).any(axis=(1, 3))
    seen = np.zeros_like(small, dtype=bool)
    out = []
    for y0, x0 in zip(*np.nonzero(small)):
        if seen[y0, x0]:
            continue
        queue, cells = deque([(y0, x0)]), []
        seen[y0, x0] = True
        while queue:
            y, x = queue.popleft()
            cells.append((y, x))
            for dy in (-1, 0, 1):
                for dx in (-1, 0, 1):
                    ny, nx = y + dy, x + dx
                    if 0 <= ny < small.shape[0] and 0 <= nx < small.shape[1] and small[ny, nx] and not seen[ny, nx]:
                        seen[ny, nx] = True
                        queue.append((ny, nx))
        if len(cells) < min_cells:
            continue
        ys, xs = zip(*cells)
        out.append((min(xs) * pool, min(ys) * pool, (max(xs) + 1) * pool, (max(ys) + 1) * pool, len(cells)))
    return sorted(out, key=lambda b: (b[1] // 200, b[0]))


def main():
    args = sys.argv[1:]
    opts = {"--tol": 40, "--pool": 4, "--min": 60}
    files = [a for a in args if not a.startswith("--") and not (args.index(a) > 0 and args[args.index(a) - 1].startswith("--"))]
    for key in opts:
        if key in args:
            opts[key] = int(args[args.index(key) + 1])
    sheet, out = files[0], files[1]
    img = Image.open(sheet).convert("RGB")
    rgb = np.asarray(img)
    boxes = blobs(foreground(rgb, opts["--tol"]), opts["--pool"], opts["--min"])
    draw = ImageDraw.Draw(img)
    for i, (x0, y0, x1, y1, area) in enumerate(boxes):
        draw.rectangle([x0, y0, x1, y1], outline=(255, 0, 0), width=3)
        draw.text((x0 + 4, y0 + 4), str(i), fill=(255, 255, 0))
        print(i, x0, y0, x1, y1, area)
    img.thumbnail((1600, 1600))
    img.save(out)


if __name__ == "__main__":
    main()
