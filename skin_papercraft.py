#!/usr/bin/env python3
"""Minecraft skin -> printable papercraft net (A4, PNG + PDF).

Usage:
    python3 skin_papercraft.py steve.png
    python3 skin_papercraft.py alex.png --model alex
    python3 skin_papercraft.py steve.png --pixel-mm 3 --credit "tg: @faustyu"
    python3 skin_papercraft.py skins/*.png --out-dir out

Supports 64x64 skins, legacy 64x32 skins and slim (Alex) arms.
Page 1: base layer nets. Solid lines = cut, dashed lines = fold, white flaps = glue.
Second layer (hat, jacket, sleeves, pants) comes as separate pieces packed into free
space, slightly bigger than the base box like in game: cut along the outline, fold on
dashes, glue on top of the assembled model.
"""
import argparse
import sys
import zlib
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont

A4_MM = (210, 297)
# Second-layer pixels fainter than this are not drawn (the game discards them too).
ALPHA_CUTOFF = 26
# How far the second layer sticks out of the base box, in skin pixels (same as in game).
INFLATE = {"head": 0.5}
INFLATE_DEFAULT = 0.25

# Box UV origins in the skin texture: (u, v) of base layer and overlay layer.
PARTS = {
    "head":      {"base": (0, 0),   "overlay": (32, 0),  "size": (8, 8, 8)},
    "body":      {"base": (16, 16), "overlay": (16, 32), "size": (8, 12, 4)},
    "right_arm": {"base": (40, 16), "overlay": (40, 32), "size": (4, 12, 4)},
    "left_arm":  {"base": (32, 48), "overlay": (48, 48), "size": (4, 12, 4)},
    "right_leg": {"base": (0, 16),  "overlay": (0, 32),  "size": (4, 12, 4)},
    "left_leg":  {"base": (16, 48), "overlay": (0, 48),  "size": (4, 12, 4)},
}

FONT_CANDIDATES = [
    "/System/Library/Fonts/Supplemental/Arial Bold.ttf",
    "/System/Library/Fonts/Supplemental/Arial.ttf",
    "/Library/Fonts/Arial Unicode.ttf",
    "C:/Windows/Fonts/arialbd.ttf",
    "C:/Windows/Fonts/arial.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    "DejaVuSans-Bold.ttf",
]


# ---------------------------------------------------------------- skin reading

def load_skin(path):
    skin = Image.open(path).convert("RGBA")
    if skin.size not in ((64, 64), (64, 32)):
        # HD skins (128x128 etc.): downscale to the standard grid.
        w, h = skin.size
        if w % 64 == 0 and h in (w, w // 2):
            skin = skin.resize((64, 64 if h == w else 32), Image.NEAREST)
        else:
            sys.exit(f"{path}: unsupported skin size {skin.size}")
    return skin


def is_slim(skin):
    """Slim (Alex) skins leave the 4th column of the right arm's back empty."""
    if skin.height != 64:
        return False
    return all(skin.getpixel((x, y))[3] == 0 for x in (54, 55) for y in range(20, 32))


def box_faces(skin, u, v, w, h, d):
    """Cut the six faces of a box, each as seen from outside the model."""
    def crop(x, y, cw, ch):
        return skin.crop((x, y, x + cw, y + ch))
    return {
        "top": crop(u + d, v, w, d),
        # The bottom texture is stored upside down relative to the net.
        "bottom": crop(u + d + w, v, w, d).transpose(Image.FLIP_TOP_BOTTOM),
        "right": crop(u, v + d, d, h),
        "front": crop(u + d, v + d, w, h),
        "left": crop(u + d + w, v + d, d, h),
        "back": crop(u + 2 * d + w, v + d, w, h),
    }


def mirror_faces(faces):
    """Legacy 64x32 skins reuse the right limb, mirrored, for the left one."""
    flip = lambda im: im.transpose(Image.FLIP_LEFT_RIGHT)
    return {
        "top": flip(faces["top"]), "bottom": flip(faces["bottom"]),
        "front": flip(faces["front"]), "back": flip(faces["back"]),
        "right": flip(faces["left"]), "left": flip(faces["right"]),
    }


def opaque(im):
    """The game draws the base layer without transparency."""
    im = im.copy()
    im.putalpha(255)
    return im


def part_size(name, slim):
    w, h, d = PARTS[name]["size"]
    if slim and name.endswith("arm"):
        w = 3
    return w, h, d


def overlay_faces(skin, name, size):
    """Second-layer faces (hat, jacket, sleeves, pants), or None if the layer is empty."""
    legacy = skin.height == 32
    if legacy and name != "head":
        return None
    over = box_faces(skin, *PARTS[name]["overlay"], *size)
    alphas = [f.getextrema()[3] for f in over.values()]
    # Notch transparency hack: a fully opaque legacy hat is treated as empty.
    if legacy and all(lo == 255 for lo, hi in alphas):
        return None
    if all(hi < ALPHA_CUTOFF for lo, hi in alphas):
        return None
    return over


def part_faces(skin, name, slim, use_overlay):
    """Base-layer faces of a part, optionally with the second layer painted on top."""
    size = part_size(name, slim)
    if skin.height == 32 and name.startswith("left_"):
        right = name.replace("left_", "right_")
        return mirror_faces(part_faces(skin, right, slim, use_overlay)[0]), size

    base = {k: opaque(f) for k, f in box_faces(skin, *PARTS[name]["base"], *size).items()}
    if use_overlay:
        over = overlay_faces(skin, name, size)
        if over:
            base = {k: Image.alpha_composite(base[k], over[k]) for k in base}
    return base, size


# ---------------------------------------------------------------- net geometry

ROW_ORDERS = {
    # Faces go around the box in this order when seen from outside.
    "rflb": ["right", "front", "left", "back"],
    "brfl": ["back", "right", "front", "left"],
}


def build_net(faces, size, order, tab_side):
    """Return a net description in skin-pixel units.

    faces: list of (image, x, y); tabs: list of polygons; cut/fold: list of segments.
    """
    w, h, d = size
    tab = 4 if d >= 8 else 3
    widths = {"right": d, "front": w, "left": d, "back": w}
    x0 = tab if tab_side == "left" else 0

    placed, tabs, cut, fold = [], [], [], []
    row_top, row_bot = d, d + h
    x = x0
    columns = {}
    for name in ROW_ORDERS[order]:
        fw = widths[name]
        columns[name] = (x, x + fw)
        placed.append((faces[name], x, row_top))
        x += fw
    x_end = x

    fx0, fx1 = columns["front"]
    placed.append((faces["top"], fx0, 0))
    placed.append((faces["bottom"], fx0, row_bot))

    # Fold lines: every internal face border.
    fold.append(((x0, row_top), (x_end, row_top)))
    fold.append(((x0, row_bot), (x_end, row_bot)))
    for name, (a, b) in columns.items():
        if a != x0:
            fold.append(((a, row_top), (a, row_bot)))

    # Cut lines: free edges of top/bottom faces.
    for y_out, y_in in ((0, row_top), (row_bot + d, row_bot)):
        cut.append(((fx0, y_out), (fx1, y_out)))
        cut.append(((fx0, y_out), (fx0, y_in)))
        cut.append(((fx1, y_out), (fx1, y_in)))

    # Side glue flap on one end, the other end is a cut.
    if tab_side == "left":
        tabs.append(flap((x0, row_bot), (x0, row_top), (-1, 0), tab))
        fold.append(((x0, row_top), (x0, row_bot)))
        cut.append(((x_end, row_top), (x_end, row_bot)))
    else:
        tabs.append(flap((x_end, row_top), (x_end, row_bot), (1, 0), tab))
        fold.append(((x_end, row_top), (x_end, row_bot)))
        cut.append(((x0, row_top), (x0, row_bot)))

    # Top/bottom flaps on every row face except the front.
    for name, (a, b) in columns.items():
        if name == "front":
            continue
        tabs.append(flap((a, row_top), (b, row_top), (0, -1), tab))
        tabs.append(flap((a, row_bot), (b, row_bot), (0, 1), tab))

    width = x_end + (tab if tab_side == "right" else 0)
    height = row_bot + d
    return {"kind": "base", "faces": placed, "tabs": tabs, "cut": cut, "fold": fold, "size": (width, height)}


def flap(p0, p1, normal, depth):
    """Glue flap on edge p0-p1: triangle on short edges, trapezoid on long ones."""
    (x0, y0), (x1, y1) = p0, p1
    length = abs(x1 - x0) + abs(y1 - y0)
    dx, dy = (x1 - x0) / length, (y1 - y0) / length
    nx, ny = normal
    if length <= 4:
        mx, my = (x0 + x1) / 2, (y0 + y1) / 2
        return [p0, (mx + nx * depth, my + ny * depth), p1]
    inset = min(depth, length / 4)
    return [
        p0,
        (x0 + dx * inset + nx * depth, y0 + dy * inset + ny * depth),
        (x1 - dx * inset + nx * depth, y1 - dy * inset + ny * depth),
        p1,
    ]


def build_shell_net(faces, size, inflate, order):
    """Net of the second layer: a slightly bigger box with no glue flaps.

    Only its opaque pixels get printed and cut out; the pieces are glued straight
    onto the assembled base model. Faces are (image, x0, y0, x1, y1) in skin-pixel units.
    """
    w, h, d = (n + 2 * inflate for n in size)
    widths = {"right": d, "front": w, "left": d, "back": w}
    placed, fold, columns = [], [], {}
    x = 0
    for name in ROW_ORDERS[order]:
        columns[name] = (x, x + widths[name])
        placed.append((faces[name], x, d, x + widths[name], d + h))
        if x:
            fold.append(((x, d), (x, d + h)))
        x += widths[name]

    fx0, fx1 = columns["front"]
    placed.append((faces["top"], fx0, 0, fx1, d))
    placed.append((faces["bottom"], fx0, d + h, fx1, 2 * d + h))
    fold.append(((fx0, d), (fx1, d)))
    fold.append(((fx0, d + h), (fx1, d + h)))
    return {"kind": "shell", "faces": placed, "fold": fold, "size": (x, 2 * d + h)}


# ---------------------------------------------------------------- drawing

def dashed_line(draw, p0, p1, width, dash, color):
    (x0, y0), (x1, y1) = p0, p1
    length = ((x1 - x0) ** 2 + (y1 - y0) ** 2) ** 0.5
    if length == 0:
        return
    steps = int(length // (dash * 2)) + 1
    for i in range(steps):
        a = i * 2 * dash / length
        b = min((i * 2 + 1) * dash / length, 1)
        if a >= 1:
            break
        draw.line(
            [(x0 + (x1 - x0) * a, y0 + (y1 - y0) * a), (x0 + (x1 - x0) * b, y0 + (y1 - y0) * b)],
            fill=color, width=width,
        )


def draw_net(page, net, origin, px, line_w, taken):
    """Draw a base-layer net; mark its area in the `taken` mask."""
    ox, oy = origin
    draw, tdraw = ImageDraw.Draw(page), ImageDraw.Draw(taken)
    pt = lambda p: (ox + p[0] * px, oy + p[1] * px)

    for poly in net["tabs"]:
        draw.polygon([pt(p) for p in poly], fill="white")
        tdraw.polygon([pt(p) for p in poly], fill=255, outline=255, width=line_w)
        pts = [pt(p) for p in poly]
        draw.line(pts, fill="black", width=line_w, joint="curve")

    for img, x, y in net["faces"]:
        big = img.resize((img.width * px, img.height * px), Image.NEAREST)
        page.paste(big, (ox + x * px, oy + y * px), big)
        tdraw.rectangle((ox + x * px - line_w, oy + y * px - line_w,
                         ox + (x + img.width) * px + line_w, oy + (y + img.height) * px + line_w), fill=255)

    for p0, p1 in net["fold"]:
        dashed_line(draw, pt(p0), pt(p1), max(1, line_w // 2), px // 5 or 2, (90, 90, 90))
    for p0, p1 in net["cut"]:
        draw.line([pt(p0), pt(p1)], fill="black", width=line_w)


def shell_pieces(net, px, line_w):
    """Split a second-layer net into separate cut-out pieces.

    Returns RGBA sprites: texture where opaque, black cut line around it, dashed folds
    where a piece wraps over a box edge, transparent everywhere else.
    """
    pad = line_w * 2
    to_px = lambda v: round(v * px) + pad
    sw, sh = (round(n * px) + 2 * pad for n in net["size"])

    # Rasterise every opaque texel with its own id to find which ones touch.
    labels = Image.new("I", (sw, sh), 0)
    ldraw = ImageDraw.Draw(labels)
    texels = []  # (rect, rgb)
    for img, x0, y0, x1, y1 in net["faces"]:
        xs = [to_px(x0 + (x1 - x0) * i / img.width) for i in range(img.width)] + [to_px(x1)]
        ys = [to_px(y0 + (y1 - y0) * j / img.height) for j in range(img.height)] + [to_px(y1)]
        for j in range(img.height):
            for i in range(img.width):
                r, g, b, a = img.getpixel((i, j))
                if a < ALPHA_CUTOFF:
                    continue
                rgb = tuple(round(c * a / 255 + 255 * (1 - a / 255)) for c in (r, g, b))
                texels.append(((xs[i], ys[j], xs[i + 1] - 1, ys[j + 1] - 1), rgb))
                ldraw.rectangle(texels[-1][0], fill=len(texels))

    parent = list(range(len(texels) + 1))

    def find(k):
        while parent[k] != k:
            parent[k] = parent[parent[k]]
            k = parent[k]
        return k

    for k, ((X0, Y0, X1, Y1), _) in enumerate(texels, 1):
        for x, y in ((X1 + 1, (Y0 + Y1) // 2), ((X0 + X1) // 2, Y1 + 1)):  # right, below
            if x < sw and y < sh and (n := labels.getpixel((x, y))):
                parent[find(n)] = find(k)

    groups = {}
    for k, texel in enumerate(texels, 1):
        groups.setdefault(find(k), []).append(texel)

    sprites = []
    for group in groups.values():
        bx0 = min(r[0] for r, _ in group) - pad
        by0 = min(r[1] for r, _ in group) - pad
        bx1 = max(r[2] for r, _ in group) + pad + 1
        by1 = max(r[3] for r, _ in group) + pad + 1
        size = (bx1 - bx0, by1 - by0)
        color = Image.new("RGB", size, "white")
        mask = Image.new("L", size, 0)
        cdraw, mdraw = ImageDraw.Draw(color), ImageDraw.Draw(mask)
        for (X0, Y0, X1, Y1), rgb in group:
            rect = (X0 - bx0, Y0 - by0, X1 - bx0, Y1 - by0)
            cdraw.rectangle(rect, fill=rgb)
            mdraw.rectangle(rect, fill=255)

        # Cut line: a ring just outside the opaque area (also outlines holes).
        ring = ImageChops.subtract(mask.filter(ImageFilter.MaxFilter(2 * line_w + 1)), mask)
        # Fold lines: only where both sides of a box edge belong to this piece.
        folds = Image.new("L", size, 0)
        fdraw = ImageDraw.Draw(folds)
        for (x0, y0), (x1, y1) in net["fold"]:
            dashed_line(fdraw, (to_px(x0) - bx0, to_px(y0) - by0), (to_px(x1) - bx0, to_px(y1) - by0),
                        max(1, line_w // 2), px // 5 or 2, 255)
        folds = ImageChops.multiply(folds, mask.filter(ImageFilter.MinFilter(3)))

        sprite = Image.new("RGBA", size, (0, 0, 0, 0))
        sprite.paste(color, (0, 0), mask)
        sprite.paste((90, 90, 90, 255), (0, 0, *size), folds)
        sprite.paste((0, 0, 0, 255), (0, 0, *size), ring)
        sprites.append(sprite)
    return sprites


# ---------------------------------------------------------------- packing
# Free space is tracked on a coarse grid; each grid row is a Python int bitmask.

def cell_rows(mask, cell):
    """Bitmask rows of the grid cells that any non-zero mask pixel touches."""
    w, h = (-(-n // cell) for n in mask.size)
    padded = Image.new("L", (w * cell, h * cell), 0)
    padded.paste(mask, (0, 0))
    small = padded.reduce(cell).point(lambda v: 1 if v else 0)
    data = small.tobytes()
    rows = [int(data[y * w:(y + 1) * w][::-1].hex().replace("01", "1").replace("00", "0") or "0", 2)
            for y in range(h)]
    return rows, w, h


def dilate(rows, w, h, gap):
    """Grow a mask by `gap` cells on every side (it gets 2*gap bigger)."""
    grown = []
    for row in rows:
        row <<= gap
        for _ in range(gap):
            row |= (row << 1) | (row >> 1)
        grown.append(row)
    grown = [0] * gap + grown + [0] * gap
    out = [0] * len(grown)
    for i in range(len(grown)):
        for j in range(max(0, i - gap), min(len(grown), i + gap + 1)):
            out[i] |= grown[j]
    return out, w + 2 * gap, h + 2 * gap


def runs(row):
    """Runs of set bits in a mask row as (first bit, length)."""
    out, bit = [], 0
    while row:
        skip = (row & -row).bit_length() - 1
        row >>= skip
        bit += skip
        length = (~row & (row + 1)).bit_length() - 1
        out.append((bit, length))
        row >>= length
        bit += length
    return out


def smear(v, n):
    """OR of v >> k for k in 0..n-1, in log(n) steps."""
    done = 1
    while done < n:
        step = min(done, n - done)
        v |= v >> step
        done += step
    return v


def first_fit(occ, grid_w, grid_h, rows, w, h):
    """Top-most, then left-most spot where the piece overlaps nothing.

    For each y, one bit mask marks every x the piece cannot start at: a run of piece
    bits [b, b+n) at row i is blocked wherever occ[y+i] has a bit in [x+b, x+b+n).
    """
    if w > grid_w or h > grid_h:
        return None
    xs = (1 << (grid_w - w + 1)) - 1
    shape = [(i, runs(r)) for i, r in enumerate(rows) if r]
    for y in range(grid_h - h + 1):
        blocked = 0
        for i, row_runs in shape:
            o = occ[y + i]
            for bit, n in row_runs:
                blocked |= smear(o >> bit, n)
            if blocked & xs == xs:
                break
        free = ~blocked & xs
        if free:
            return (free & -free).bit_length() - 1, y
    return None


def pack(page, occ, sprites, cell, gap):
    """Place sprites into free space of `page`; return the ones that did not fit."""
    grid_h, grid_w = len(occ), -(-page.width // cell)
    left = []
    for sprite in sprites:
        best = None
        for img in (sprite, sprite.rotate(90, expand=True)):
            rows, w, h = cell_rows(img.getchannel("A"), cell)
            pos = first_fit(occ, grid_w, grid_h, *dilate(rows, w, h, gap))
            if pos and (best is None or pos[::-1] < best[0][::-1]):
                best = (pos, img, rows)
        if not best:
            left.append(sprite)
            continue
        (x, y), img, rows = best
        x, y = x + gap, y + gap
        page.paste(img, (x * cell, y * cell), img)
        for i, row in enumerate(rows):
            occ[y + i] |= row << x
    return left


# ---------------------------------------------------------------- pages

def load_font(size):
    for path in FONT_CANDIDATES:
        try:
            return ImageFont.truetype(path, size)
        except OSError:
            continue
    try:
        return ImageFont.load_default(size)
    except TypeError:
        return ImageFont.load_default()


PART_LAYOUT = [
    ("head", "rflb", "left"),
    ("body", "rflb", "left"),
    # Left column: character's right limbs (flap on the right), right column mirrored.
    ("right_arm", "brfl", "right"), ("left_arm", "rflb", "left"),
    ("right_leg", "brfl", "right"), ("left_leg", "rflb", "left"),
]
PAGE_ROWS = [["head"], ["body"], ["right_arm", "left_arm"], ["right_leg", "left_leg"]]
GAP_UNITS = 3


def page_geometry(dpi):
    page_w, page_h = (round(mm / 25.4 * dpi) for mm in A4_MM)
    return page_w, page_h, round(10 / 25.4 * dpi)


def row_height(row):
    return max(n["size"][1] for n in row)


def blank_page(dpi, credit, px):
    """White A4 page with the credit in the corner, plus a mask of what is taken."""
    page_w, page_h, margin = page_geometry(dpi)
    page = Image.new("RGB", (page_w, page_h), "white")
    taken = Image.new("L", (page_w, page_h), 255)
    ImageDraw.Draw(taken).rectangle((margin, margin, page_w - margin - 1, page_h - margin - 1), fill=0)
    if credit:
        ImageDraw.Draw(page).text((margin, margin // 2), credit, fill="black",
                                  font=load_font(round(px * 1.2)), anchor="lm")
    return page, taken


def base_page(nets, px, dpi, credit):
    page_w, _, margin = page_geometry(dpi)
    page, taken = blank_page(dpi, credit, px)
    line_w = max(2, px // 12)
    y = margin
    for row in PAGE_ROWS:
        row = [nets[p] for p in row]
        xs = ([(page_w - row[0]["size"][0] * px) // 2] if len(row) == 1
              else [margin, page_w - margin - row[1]["size"][0] * px])
        for net, x in zip(row, xs):
            draw_net(page, net, (x, y), px, line_w, taken)
        y += row_height(row) * px + GAP_UNITS * px
    return page, taken


def render(skin, pixel_mm, dpi, layers, force_slim, credit):
    """Return (pages, slim). layers: 'separate' = second layer as cut-out pieces packed
    into free space, 'flat' = painted onto the base, 'none' = ignored."""
    slim = force_slim if force_slim is not None else is_slim(skin)

    base, shells = {}, []
    for part, order, side in PART_LAYOUT:
        faces, size = part_faces(skin, part, slim, use_overlay=layers == "flat")
        base[part] = build_net(faces, size, order, side)
        over = overlay_faces(skin, part, size) if layers == "separate" else None
        if over:
            shells.append(build_shell_net(over, size, INFLATE.get(part, INFLATE_DEFAULT), order))

    _, page_h, margin = page_geometry(dpi)
    px = round(pixel_mm / 25.4 * dpi)
    rows = [[base[p] for p in row] for row in PAGE_ROWS]
    max_px = int((page_h - 2 * margin) / (sum(map(row_height, rows)) + GAP_UNITS * (len(rows) - 1)))
    if px > max_px:
        print(f"  pixel size reduced to {max_px / dpi * 25.4:.2f} mm to fit A4", file=sys.stderr)
        px = max_px
    line_w = max(2, px // 12)

    page, taken = base_page(base, px, dpi, credit)
    pages = [(page, taken)]
    sprites = [s for net in shells for s in shell_pieces(net, px, line_w)]
    sprites.sort(key=lambda s: s.width * s.height, reverse=True)

    cell = max(4, round(dpi / 25.4))  # ~1 mm grid
    gap = 2                           # ~2 mm between pieces
    while sprites:
        page, taken = pages[-1]
        occ, _, _ = cell_rows(taken, cell)
        placed_before = len(sprites)
        sprites = pack(page, occ, sprites, cell, gap)
        if sprites:
            if len(sprites) == placed_before and len(pages) > 1:
                sys.exit("  a second-layer piece is bigger than a whole page")
            pages.append(blank_page(dpi, credit, px))
    return [p for p, _ in pages], slim


class PdfWriter:
    """Lossless PDF (Pillow's writer uses JPEG, which smears pixel art), built one page at
    a time so finished pages do not have to stay in memory."""

    def __init__(self, dpi):
        self.dpi = dpi
        self.objects = [b"<< /Type /Catalog /Pages 2 0 R >>", None]
        self.kids = []

    def add(self, page):
        w, h = page.size
        pw, ph = w * 72 / self.dpi, h * 72 / self.dpi
        # Compressed in strips: the whole page as bytes would be another 26 MB at 300 dpi.
        page = page if page.mode == "RGB" else page.convert("RGB")
        z, data = zlib.compressobj(6), []
        for y in range(0, h, 256):
            data.append(z.compress(page.crop((0, y, w, min(h, y + 256))).tobytes()))
        data = b"".join(data) + z.flush()
        content = f"q {pw:.2f} 0 0 {ph:.2f} 0 0 cm /Im0 Do Q".encode()
        n = len(self.objects) + 1  # object number of this page
        self.kids.append(f"{n} 0 R")
        self.objects += [
            f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 {pw:.2f} {ph:.2f}] "
            f"/Resources << /XObject << /Im0 {n + 1} 0 R >> >> /Contents {n + 2} 0 R >>".encode(),
            f"<< /Type /XObject /Subtype /Image /Width {w} /Height {h} /ColorSpace /DeviceRGB "
            f"/BitsPerComponent 8 /Filter /FlateDecode /Length {len(data)} >>\nstream\n".encode()
            + data + b"\nendstream",
            f"<< /Length {len(content)} >>\nstream\n".encode() + content + b"\nendstream",
        ]

    def save(self, path):
        objects = self.objects[:]
        objects[1] = f"<< /Type /Pages /Kids [{' '.join(self.kids)}] /Count {len(self.kids)} >>".encode()
        with open(path, "wb") as f:
            f.write(b"%PDF-1.4\n")
            offsets = []
            for i, obj in enumerate(objects, 1):
                offsets.append(f.tell())
                f.write(f"{i} 0 obj\n".encode() + obj + b"\nendobj\n")
            xref = f.tell()
            f.write(f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode())
            f.write(b"".join(f"{o:010d} 00000 n \n".encode() for o in offsets))
            f.write(f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())


def save_pdf(pages, path, dpi):
    pdf = PdfWriter(dpi)
    for page in pages:
        pdf.add(page)
    pdf.save(path)


MODELS = {"auto": None, "steve": False, "alex": True}


def main():
    ap = argparse.ArgumentParser(description="Minecraft skin -> papercraft net")
    ap.add_argument("skins", nargs="+", help="skin PNG files (64x64 or 64x32)")
    ap.add_argument("--credit", default="tg: @faustyu", help='text in the top corner of every page ("" = none)')
    ap.add_argument("--pixel-mm", type=float, default=2.5, help="size of one skin pixel on paper, mm (default 2.5)")
    ap.add_argument("--dpi", type=int, default=300)
    ap.add_argument("--layers", choices=["separate", "flat", "none"], default="separate",
                    help="second layer (hat, jacket, sleeves, pants): separate = cut-out pieces packed into free "
                         "space (default), flat = painted onto the base, none = ignored")
    ap.add_argument("--model", choices=["auto", "steve", "alex"], default="auto",
                    help="arm width: steve = 4 px, alex = 3 px (slim), auto = detect from the skin")
    ap.add_argument("--out-dir", type=Path, help="output folder (default: next to the skin)")
    args = ap.parse_args()

    for path in map(Path, args.skins):
        skin = load_skin(path)
        pages, slim_used = render(skin, args.pixel_mm, args.dpi, args.layers, MODELS[args.model], args.credit)
        out_dir = args.out_dir or path.parent
        out_dir.mkdir(parents=True, exist_ok=True)
        stem = out_dir / f"{path.stem}_papercraft"
        pngs = [stem.with_suffix(".png")] + [out_dir / f"{stem.name}_page{i}.png" for i in range(2, len(pages) + 1)]
        for page, png in zip(pages, pngs):
            page.save(png, dpi=(args.dpi, args.dpi))
        save_pdf(pages, stem.with_suffix(".pdf"), args.dpi)
        files = ", ".join(p.name for p in pngs + [stem.with_suffix(".pdf")])
        print(f"{path.name}: {'alex' if slim_used else 'steve'} model, {len(pages)} page(s) -> {files}")


if __name__ == "__main__":
    main()
