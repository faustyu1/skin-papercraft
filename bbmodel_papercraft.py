#!/usr/bin/env python3
"""Blockbench model (.bbmodel) -> printable papercraft net (A4, PNG + PDF).

Usage:
    python3 bbmodel_papercraft.py model.bbmodel
    python3 bbmodel_papercraft.py model.bbmodel --unit-mm 6 --author "t.me/someone"

Every cube becomes a box net (solid lines = cut, dashed = fold, white flaps = glue).
Flat cubes (planes) become fold-over pieces: fold on the dashed line and glue the two
halves back to back. Planes with see-through texture are cut out along their shape.
Cube rotations are not part of the net: the last page shows the assembled model with
every piece numbered, use it as the assembly reference.
Pieces hidden inside another cube and watermark elements are skipped.
"""
import argparse
from array import array
import base64
import io
import json
import math
import re
import sys
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter

from skin_papercraft import (
    ALPHA_CUTOFF, PdfWriter, blank_page, build_net, cell_rows, dashed_line, load_font, pack, page_geometry,
)

# Texels per model unit when a face texture is resampled for print or preview.
WATERMARK = re.compile(r"watermark|вотермарк|ватермарк", re.I)
EPS = 1e-6


# ---------------------------------------------------------------- model reading

def load_model(path):
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    textures = []
    for t in data.get("textures", []):
        src = t.get("source", "")
        if not src.startswith("data:"):
            sys.exit(f"{path}: texture {t.get('name')!r} is not embedded in the model")
        img = Image.open(io.BytesIO(base64.b64decode(src.split(",", 1)[1]))).convert("RGBA")
        res = data.get("resolution", {})
        uv_w = t.get("uv_width") or res.get("width") or img.width
        uv_h = t.get("uv_height") or res.get("height") or img.height
        textures.append((img, img.width / uv_w, img.height / uv_h))

    groups = {g["uuid"]: g for g in data.get("groups", [])}
    parents, paths = {}, {}

    def walk(nodes, chain):
        for node in nodes:
            if isinstance(node, str):
                parents[node] = chain
            else:
                g = groups.get(node["uuid"], node)
                walk(node.get("children", []), chain + [g])

    walk(data.get("outliner", []), [])
    elements = []
    for e in data.get("elements", []):
        if e.get("type", "cube") != "cube":
            continue
        chain = parents.get(e["uuid"], [])
        e["groups"] = chain
        elements.append(e)
    return elements, textures


def box(e):
    inf = e.get("inflate") or 0
    lo = [a - inf for a in e["from"]]
    hi = [b + inf for b in e["to"]]
    return lo, hi


def is_rotated(obj):
    return any(abs(r) > EPS for r in obj.get("rotation") or (0, 0, 0))


def hidden_inside(e, others, clear=frozenset()):
    """A cube fully enclosed by another opaque cube of the same group is never seen.
    Cubes in `clear` (ids of see-through cubes, like a skin's outer layer) hide nothing."""
    lo, hi = box(e)
    for o in others:
        if o is e or id(o) in clear or o["groups"] != e["groups"] or is_rotated(o) or is_rotated(e):
            continue
        olo, ohi = box(o)
        if all(ol <= l + EPS and h <= oh + EPS for l, h, ol, oh in zip(lo, hi, olo, ohi)) and (lo, hi) != (olo, ohi):
            return True
    return False


def texel_size(textures, face):
    """Size of a face's texture region in texture pixels, as the face is oriented: the
    preview needs no more, and big models would take gigabytes at a fixed density."""
    if not face or face.get("texture") is None or not face.get("uv"):
        return 1, 1
    _, sx, sy = textures[face["texture"]]
    u0, v0, u1, v1 = face["uv"]
    w, h = max(1, round(abs(u1 - u0) * sx)), max(1, round(abs(v1 - v0) * sy))
    return (w, h) if face.get("rotation", 0) % 180 == 0 else (h, w)


def face_image(textures, face, w, h):
    """Face texture resampled to w x h pixels, oriented as Blockbench shows it."""
    if not face or face.get("texture") is None or w <= 0 or h <= 0:
        return None
    img, sx, sy = textures[face["texture"]]
    u0, v0, u1, v1 = face["uv"]
    rot = face.get("rotation", 0) % 360
    size = (w, h) if rot in (0, 180) else (h, w)
    out = img.transform(size, Image.Transform.EXTENT, (u0 * sx, v0 * sy, u1 * sx, v1 * sy), Image.NEAREST)
    return out.rotate(-rot, expand=True) if rot else out


def face_dims(e):
    """Size (width, height) in model units of each face as seen from outside."""
    lo, hi = box(e)
    w, h, d = (b - a for a, b in zip(lo, hi))
    return {"north": (w, h), "south": (w, h), "east": (d, h), "west": (d, h), "up": (w, d), "down": (w, d)}


# ---------------------------------------------------------------- 3D preview

def mat_mul(a, b):
    return [[sum(a[i][k] * b[k][j] for k in range(3)) for j in range(3)] for i in range(3)]


def rot_matrix(deg):
    x, y, z = (math.radians(a) for a in deg)
    rx = [[1, 0, 0], [0, math.cos(x), -math.sin(x)], [0, math.sin(x), math.cos(x)]]
    ry = [[math.cos(y), 0, math.sin(y)], [0, 1, 0], [-math.sin(y), 0, math.cos(y)]]
    rz = [[math.cos(z), -math.sin(z), 0], [math.sin(z), math.cos(z), 0], [0, 0, 1]]
    return mat_mul(rz, mat_mul(ry, rx))  # Blockbench applies X, then Y, then Z


def apply(m, p, origin=(0, 0, 0)):
    q = [p[i] - origin[i] for i in range(3)]
    return tuple(sum(m[i][k] * q[k] for k in range(3)) + origin[i] for i in range(3))


def transform(e):
    """The cube rotation, then every parent group rotation, innermost first, folded into
    one rotation m and offset t (world = m·p + t). Computed once per cube."""
    if "_xf" not in e:
        m, t = [[1, 0, 0], [0, 1, 0], [0, 0, 1]], (0, 0, 0)
        for obj in [e] + e["groups"][::-1]:
            if is_rotated(obj):
                r, o = rot_matrix(obj["rotation"]), obj.get("origin", (0, 0, 0))
                m = mat_mul(r, m)
                t = apply(r, t, o)
        e["_xf"] = (m, t)
    return e["_xf"]


def to_world(e, p):
    (a, b, c), t = transform(e)
    return (a[0] * p[0] + a[1] * p[1] + a[2] * p[2] + t[0],
            b[0] * p[0] + b[1] * p[1] + b[2] * p[2] + t[1],
            c[0] * p[0] + c[1] * p[1] + c[2] * p[2] + t[2])


def face_corners(e):
    """Corners (top-left, top-right, bottom-left) of each face as its texture lies on it."""
    (x0, y0, z0), (x1, y1, z1) = box(e)
    return {
        "north": [(x1, y1, z0), (x0, y1, z0), (x1, y0, z0)],
        "south": [(x0, y1, z1), (x1, y1, z1), (x0, y0, z1)],
        "east": [(x1, y1, z1), (x1, y1, z0), (x1, y0, z1)],
        "west": [(x0, y1, z0), (x0, y1, z1), (x0, y0, z0)],
        "up": [(x0, y1, z0), (x1, y1, z0), (x0, y1, z1)],
        "down": [(x0, y0, z1), (x1, y0, z1), (x0, y0, z0)],
    }


def render_preview(elements, textures, size, yaw=30, pitch=-20):
    """Orthographic render of the model seen from the front, a bit from the left and above.

    Returns (image, owner): owner[y * size + x] is the index of the element seen at
    that pixel, or -1.
    """
    view = mat_mul(rot_matrix((pitch, 0, 0)), rot_matrix((0, yaw, 0)))
    # The camera looks from -Z (the front); +X is on the viewer's left.
    cam = lambda p: (lambda q: (-q[0], -q[1], q[2]))(apply(view, p))

    quads = []
    for index, e in enumerate(elements):
        dims = face_dims(e)
        for name, corners in face_corners(e).items():
            fw, fh = dims[name]
            if fw < EPS or fh < EPS:
                continue
            face = e["faces"].get(name)
            img = face_image(textures, face, *texel_size(textures, face))
            if img is not None:
                quads.append(([cam(to_world(e, c)) for c in corners], img, index))
    if not quads:
        sys.exit("model has no textured faces")

    xs = [p[0] for q, *_ in quads for p in q] + [q[1][0] + q[2][0] - q[0][0] for q, *_ in quads]
    ys = [p[1] for q, *_ in quads for p in q] + [q[1][1] + q[2][1] - q[0][1] for q, *_ in quads]
    scale = 0.92 * size / max(max(xs) - min(xs), max(ys) - min(ys))
    cx, cy = (max(xs) + min(xs)) / 2, (max(ys) + min(ys)) / 2
    screen = lambda p: (size / 2 + (p[0] - cx) * scale, size / 2 + (p[1] - cy) * scale)

    # Plain z-buffer: faces of thin decals sit a hair above other faces, painter's
    # sorting gets those wrong.
    # Flat arrays: lists of tuples for a 1000 px view would take ~100 MB.
    color = bytearray(b"\xff") * (3 * size * size)
    depth = array("d", [math.inf]) * (size * size)
    owner = array("i", [-1]) * (size * size)
    for pts, img, index in quads:
        (ax, ay), (bx, by), (lx, ly) = (screen(p) for p in pts)
        az, bz, lz = (p[2] for p in pts)
        ex, ey, fx, fy = bx - ax, by - ay, lx - ax, ly - ay
        det = ex * fy - ey * fx
        if abs(det) < 1e-6:
            continue
        # Light from the viewer: faces turned away get darker.
        (px, py, pz), (qx, qy, qz), (rx, ry, rz) = pts
        n = ((qy - py) * (rz - pz) - (qz - pz) * (ry - py),
             (qz - pz) * (rx - px) - (qx - px) * (rz - pz),
             (qx - px) * (ry - py) - (qy - py) * (rx - px))
        shade = 0.7 + 0.3 * abs(n[2]) / (math.hypot(*n) or 1)
        tex, (tw, th) = img.load(), img.size
        xs, ys = (ax, bx, lx, bx + fx), (ay, by, ly, by + fy)
        for Y in range(max(0, int(min(ys))), min(size, int(max(ys)) + 1)):
            ry_ = Y + 0.5 - ay
            for X in range(max(0, int(min(xs))), min(size, int(max(xs)) + 1)):
                rx_ = X + 0.5 - ax
                s_ = (rx_ * fy - ry_ * fx) / det
                t_ = (ex * ry_ - ey * rx_) / det
                if not (0 <= s_ < 1 and 0 <= t_ < 1):
                    continue
                z = az + s_ * (bz - az) + t_ * (lz - az)
                k = Y * size + X
                if z >= depth[k]:
                    continue
                r, g, b, al = tex[int(s_ * tw), int(t_ * th)]
                if al >= 128:
                    depth[k] = z
                    owner[k] = index
                    color[3 * k:3 * k + 3] = bytes((int(r * shade), int(g * shade), int(b * shade)))
    return Image.frombytes("RGB", (size, size), bytes(color)), owner


# ---------------------------------------------------------------- pieces
# Every piece is an RGBA sprite: opaque where there is paper (or a label), transparent
# elsewhere, so the packer from skin_papercraft can fit pieces into each other's gaps.

# Box net faces in skin_papercraft terms: the ring goes east, north, west, south as seen
# from outside, the lid and the bottom hang off the north face turned by 180 degrees.
BOX_FACES = {"right": "east", "front": "north", "left": "west", "back": "south", "top": "up", "bottom": "down"}
# Flat cubes: which two faces form the piece and whether they sit side by side.
PLANE_FACES = {0: ("east", "west", True), 1: ("up", "down", False), 2: ("north", "south", True)}


class Sheet:
    """Drawing context of one sprite, in millimetres."""

    def __init__(self, geom_mm, px_mm, dpi, label):
        (x0, y0), (x1, y1) = geom_mm
        self.k = px_mm
        self.line_w = max(2, round(0.2 * px_mm))
        self.dash = max(2, round(0.6 * px_mm))
        self.font = load_font(round(2.8 * px_mm))
        pad = self.line_w * 2
        label_h = round(3.6 * px_mm)
        self.ox, self.oy = pad - x0 * px_mm, pad + label_h - y0 * px_mm
        size = (round((x1 - x0) * px_mm) + 2 * pad, round((y1 - y0) * px_mm) + 2 * pad + label_h)
        self.img = Image.new("RGBA", size, (0, 0, 0, 0))
        self.draw = ImageDraw.Draw(self.img)
        self.draw.text((pad, label_h // 2), label, fill="black", font=self.font, anchor="lm")

    def pt(self, p):
        return self.ox + p[0] * self.k, self.oy + p[1] * self.k

    def rect(self, x, y, w, h):
        """Pixel box of a face; neighbouring faces share their border pixels exactly."""
        X0, Y0 = (round(v) for v in self.pt((x, y)))
        X1, Y1 = (round(v) for v in self.pt((x + w, y + h)))
        return X0, Y0, X1, Y1

    def face(self, img, x, y, w, h):
        X0, Y0, X1, Y1 = self.rect(x, y, w, h)
        tile = Image.new("RGBA", (max(1, X1 - X0), max(1, Y1 - Y0)), "white")
        if img is not None:
            tile.alpha_composite(img.resize(tile.size, Image.NEAREST))
        self.img.paste(tile, (X0, Y0))

    def tab(self, poly):
        self.draw.polygon([self.pt(p) for p in poly], fill="white")
        self.draw.line([self.pt(p) for p in poly], fill="black", width=self.line_w, joint="curve")

    def fold(self, p0, p1):
        dashed_line(self.draw, self.pt(p0), self.pt(p1), max(1, self.line_w // 2), self.dash, (90, 90, 90, 255))

    def cut(self, p0, p1):
        self.draw.line([self.pt(p0), self.pt(p1)], fill="black", width=self.line_w)


def bounds(points):
    xs, ys = [p[0] for p in points], [p[1] for p in points]
    return (min(xs), min(ys)), (max(xs), max(ys))


def sample(textures, face, w_mm, h_mm, px_mm, turn=False):
    img = face_image(textures, face, max(1, round(w_mm * px_mm)), max(1, round(h_mm * px_mm)))
    return img.rotate(180) if img is not None and turn else img


def box_piece(e, textures, unit, px_mm, dpi, label):
    lo, hi = box(e)
    size = tuple((b - a) * unit for a, b in zip(lo, hi))
    net = build_net(BOX_FACES, size, "rflb", "left")
    dims = {k: (d[0] * unit, d[1] * unit) for k, d in face_dims(e).items()}

    points = [p for poly in net["tabs"] for p in poly] + [(0, 0), net["size"]]
    sheet = Sheet(bounds(points), px_mm, dpi, label)
    for poly in net["tabs"]:
        sheet.tab(poly)
    for name, x, y in net["faces"]:
        w, h = dims[name]
        img = sample(textures, e["faces"].get(name), w, h, px_mm, turn=name in ("up", "down"))
        sheet.face(img, x, y, w, h)
    for p0, p1 in net["fold"]:
        sheet.fold(p0, p1)
    for p0, p1 in net["cut"]:
        sheet.cut(p0, p1)
    return [sheet.img]


def see_through(img):
    return img is not None and img.getchannel("A").getextrema()[0] < ALPHA_CUTOFF


def clear_cube(e, textures):
    """A solid cube with see-through texture somewhere: an outer layer, a cage, glass."""
    dims = face_dims(e)
    if min(size_of(e)) < LAYER_MIN:
        return False
    for name in dims:
        face = e["faces"].get(name)
        img = face_image(textures, face, *texel_size(textures, face))
        if see_through(img):
            return True
    return False


def wrapped(e, others, tol=0.05):
    """Biggest cube that e's box encloses, if e is an outer layer around it."""
    lo, hi = world_box(e)
    inner = [o for o in others if o is not e and not o.get("over")
             and all(lo[i] - tol <= a and b <= hi[i] + tol for i, (a, b) in enumerate(zip(*world_box(o))))
             and min(size_of(o)) >= LAYER_MIN]
    return max(inner, key=lambda o: math.prod(size_of(o)), default=None)


# Outer layers and what they wrap are real cubes, not decals: at least this thick (units).
LAYER_MIN = 0.5

FACE_WORDS = {"north": "перед", "south": "зад", "east": "право", "west": "лево", "up": "верх", "down": "низ"}


def overlay_pieces(e, textures, unit, px_mm, dpi, label):
    """An outer layer (hat, jacket, sleeves) printed as one cut-out per face, glued flat
    over the matching face of the cube inside, like the second layer of a skin."""
    pieces = []
    for name, (fw, fh) in face_dims(e).items():
        w, h = fw * unit, fh * unit
        img = sample(textures, e["faces"].get(name), w, h, px_mm)
        if img is None or img.getchannel("A").getextrema()[1] < ALPHA_CUTOFF:
            continue
        pieces.append(cutout_piece(img, w, h, px_mm, dpi, f"{label} {FACE_WORDS[name]}", flap=False))
    return pieces


def plane_piece(e, textures, unit, px_mm, dpi, label, axis):
    first, second, side_by_side = PLANE_FACES[axis]
    dims = face_dims(e)
    w, h = (v * unit for v in dims[first])
    names = [n for n in (first, second)]
    imgs = [sample(textures, e["faces"].get(n), w, h, px_mm) for n in names]
    keep = [(im, n) for im, n in zip(imgs, names) if im is not None and im.getchannel("A").getextrema()[1] >= ALPHA_CUTOFF]
    if not keep:
        return []
    imgs = [im for im, _ in keep]
    if e.get("plane_cut"):
        # Part of the plane sinks into a solid piece: print only what stays outside and
        # glue it on along the cut line.
        poly3d, glue = e["plane_cut"]
        pieces = []
        for img, name in keep:
            tl, tr, bl = face_corners(e)[name]
            ux, vy = v_sub(tr, tl), v_sub(bl, tl)
            st = lambda q: (v_dot(v_sub(q, tl), ux) / v_dot(ux, ux), v_dot(v_sub(q, tl), vy) / v_dot(vy, vy))
            mask = Image.new("L", img.size, 0)
            ImageDraw.Draw(mask).polygon([(a * img.width, b * img.height) for a, b in map(st, poly3d)], fill=255)
            img = img.copy()
            img.putalpha(ImageChops.multiply(img.getchannel("A"), mask))
            seg = None
            if glue:
                (a0, b0), (a1, b1) = st(glue[0]), st(glue[1])
                cs = [st(q) for q in poly3d]
                centre = (sum(c[0] for c in cs) / len(cs) * w, sum(c[1] for c in cs) / len(cs) * h)
                seg = ((a0 * w, b0 * h), (a1 * w, b1 * h), centre)
            pieces.append(cutout_piece(img, w, h, px_mm, dpi, label, seg))
        return pieces
    if any(see_through(im) for im in imgs):
        return [cutout_piece(im, w, h, px_mm, dpi, label) for im in imgs]

    # Fold-over piece: the second face hangs off the first one, fold and glue back to back.
    dx, dy = (w, 0) if side_by_side else (0, h)
    rects = [(0, 0), (dx, dy)][:len(imgs)]
    sheet = Sheet(bounds([(0, 0), (w + dx * (len(imgs) - 1), h + dy * (len(imgs) - 1))]), px_mm, dpi, label)
    for img, (x, y) in zip(imgs, rects):
        sheet.face(img, x, y, w, h)
    W, H = w + dx * (len(imgs) - 1), h + dy * (len(imgs) - 1)
    for p0, p1 in (((0, 0), (W, 0)), ((W, 0), (W, H)), ((W, H), (0, H)), ((0, H), (0, 0))):
        sheet.cut(p0, p1)
    if len(imgs) == 2:
        sheet.fold((dx, dy), (w, h))
    return [sheet.img]


def cutout_piece(img, w, h, px_mm, dpi, label, seg=None, flap=True):
    """A see-through plane cut along its shape, with a glue flap on the edge that most
    of the shape touches (hair, fins, antennae grow from that edge), or on `seg`
    (p0, p1, centre of the shape) when the piece was cut off along a line."""
    alpha = img.getchannel("A").point(lambda v: 255 if v >= ALPHA_CUTOFF else 0)
    W, H = img.size
    edges = {
        "top": (alpha.crop((0, 0, W, 1)), ((0, 0), (w, 0)), (0, -1)),
        "bottom": (alpha.crop((0, H - 1, W, H)), ((w, h), (0, h)), (0, 1)),
        "left": (alpha.crop((0, 0, 1, H)), ((0, h), (0, 0)), (-1, 0)),
        "right": (alpha.crop((W - 1, 0, W, H)), ((w, 0), (w, h)), (1, 0)),
    }
    touch = {k: sum(v[0].tobytes()) / 255 / max(v[0].size) for k, v in edges.items()}
    best = max(touch, key=touch.get)
    glue = fold = None
    if seg and math.dist(seg[0], seg[1]) > 0.5:
        p0, p1, (cx, cy) = seg
        length = math.dist(p0, p1)
        nx, ny = (p1[1] - p0[1]) / length, -(p1[0] - p0[0]) / length
        if nx * (p0[0] - cx) + ny * (p0[1] - cy) < 0:
            nx, ny = -nx, -ny
        glue, fold = flap_poly(p0, p1, (nx, ny)), (p0, p1)
    elif flap and touch[best] > 0 and alpha.getbbox() != (0, 0, W, H):
        _, (p0, p1), normal = edges[best]
        glue, fold = flap_poly(p0, p1, normal), (p0, p1)

    points = [(0, 0), (w, h)] + (glue or [])
    sheet = Sheet(bounds(points), px_mm, dpi, label)
    X0, Y0, X1, Y1 = sheet.rect(0, 0, w, h)
    body = Image.new("RGBA", (X1 - X0, Y1 - Y0), "white")
    body.alpha_composite(img.resize(body.size, Image.NEAREST))
    mask = Image.new("L", sheet.img.size, 0)
    mask.paste(alpha.resize(body.size, Image.NEAREST), (X0, Y0))
    if glue:
        ImageDraw.Draw(mask).polygon([sheet.pt(p) for p in glue], fill=255)

    ring = ImageChops.subtract(mask.filter(ImageFilter.MaxFilter(2 * sheet.line_w + 1)), mask)
    paper = Image.new("RGBA", sheet.img.size, "white")
    paper.paste(body, (X0, Y0))
    label = sheet.img
    sheet.img = Image.new("RGBA", label.size, (0, 0, 0, 0))
    sheet.img.paste(paper, (0, 0), mask)
    sheet.img.paste((0, 0, 0, 255), (0, 0, *label.size), ring)
    sheet.img.alpha_composite(label)
    if glue:
        sheet.draw = ImageDraw.Draw(sheet.img)
        sheet.fold(*fold)
    return sheet.img


def flap_poly(p0, p1, normal, depth=3):
    """Glue flap as long as the whole edge, with slanted ends."""
    (x0, y0), (x1, y1) = p0, p1
    length = math.hypot(x1 - x0, y1 - y0)
    dx, dy = (x1 - x0) / length, (y1 - y0) / length
    inset = min(depth, length / 4)
    nx, ny = normal
    return [p0, (x0 + dx * inset + nx * depth, y0 + dy * inset + ny * depth),
            (x1 - dx * inset + nx * depth, y1 - dy * inset + ny * depth), p1]


def element_pieces(e, textures, unit, px_mm, dpi, label):
    if e.get("over"):
        return overlay_pieces(e, textures, unit, px_mm, dpi, label)
    lo, hi = box(e)
    # Cubes thinner than paper are printed as planes.
    flat = [i for i in range(3) if (hi[i] - lo[i]) * unit < THIN_MM]
    if len(flat) > 1:
        return []
    if flat:
        return plane_piece(e, textures, unit, px_mm, dpi, label, flat[0])
    if e.get("cut") and e["cut"][1]:
        pieces = slanted_piece(e, e["cut"][1], textures, unit, px_mm, dpi, label)
        if pieces:
            return pieces
    return box_piece(e, textures, unit, px_mm, dpi, label)


# ---------------------------------------------------------------- slanted pieces
# A tilted cube usually sticks into a neighbour (a leg into the body). The part inside
# is never seen, so it is cut off along the neighbour's face. The piece then ends in a
# slanted glue face: glued flat onto the neighbour, it stands at the model's angle.
# Geometry is done in the tilted cube's own coordinates, where it is axis-aligned.

def v_sub(a, b):
    return (a[0] - b[0], a[1] - b[1], a[2] - b[2])


def v_add(a, b):
    return (a[0] + b[0], a[1] + b[1], a[2] + b[2])


def v_mul(a, k):
    return (a[0] * k, a[1] * k, a[2] * k)


def v_dot(a, b):
    return a[0] * b[0] + a[1] * b[1] + a[2] * b[2]


def v_cross(a, b):
    return (a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0])


def v_unit(a):
    n = math.sqrt(v_dot(a, a)) or 1
    return (a[0] / n, a[1] / n, a[2] / n)


def to_local(e, p):
    """Inverse of to_world: the rotation is orthonormal, so its inverse is its transpose."""
    m, t = transform(e)
    q = (p[0] - t[0], p[1] - t[1], p[2] - t[2])
    return tuple(m[0][i] * q[0] + m[1][i] * q[1] + m[2][i] * q[2] for i in range(3))


NORMALS = {"north": (0, 0, -1), "south": (0, 0, 1), "east": (1, 0, 0), "west": (-1, 0, 0),
           "up": (0, 1, 0), "down": (0, -1, 0)}


def box_solid(e):
    """The cube as a convex solid: faces with corner points, outward normal, source face
    and its texture corners (top-left, top-right, bottom-left)."""
    faces = []
    for name, (tl, tr, bl) in face_corners(e).items():
        br = v_add(tr, v_sub(bl, tl))
        faces.append({"pts": [tl, tr, br, bl], "n": NORMALS[name], "src": name, "tex": (tl, tr, bl)})
    return faces


def poly_area(pts, n):
    total = (0, 0, 0)
    for a, b in zip(pts, pts[1:] + pts[:1]):
        total = v_add(total, v_cross(a, b))
    return abs(v_dot(total, n)) / 2


def volume(solid):
    return sum(poly_area(f["pts"], f["n"]) * v_dot(f["n"], f["pts"][0]) for f in solid) / 3


def clip(solid, p0, n, eps=1e-5):
    """Keep the part of a convex solid behind the plane (p0, outward normal n)."""
    out, cap, flush = [], [], False
    for f in solid:
        pts, kept = f["pts"], []
        if all(abs(v_dot(n, v_sub(q, p0))) <= eps for q in pts):
            # The face lies on the plane: it is the cap itself, or the solid is flat there.
            if v_dot(f["n"], n) > 0:
                out.append(f)
                flush = True
            continue
        for a, b in zip(pts, pts[1:] + pts[:1]):
            da, db = v_dot(n, v_sub(a, p0)), v_dot(n, v_sub(b, p0))
            if da <= eps:
                kept.append(a)
                if da >= -eps:
                    cap.append(a)
            if (da < -eps < eps < db) or (db < -eps < eps < da):
                x = v_add(a, v_mul(v_sub(b, a), da / (da - db)))
                kept.append(x)
                cap.append(x)
        kept = [q for i, q in enumerate(kept) if key(q) != key(kept[i - 1])] if len(kept) > 1 else kept
        if len(kept) >= 3 and poly_area(kept, f["n"]) > eps:
            out.append({**f, "pts": kept})
    cap = list({key(p): p for p in cap}.values())
    if len(cap) >= 3 and not flush:
        c = v_mul(tuple(map(sum, zip(*cap))), 1 / len(cap))
        u = v_unit(v_sub(cap[0], c))
        w = v_cross(n, u)
        cap.sort(key=lambda p: math.atan2(v_dot(v_sub(p, c), w), v_dot(v_sub(p, c), u)))
        if poly_area(cap, n) > eps:
            out.append({"pts": cap, "n": n, "src": None})
    # A single face is a polygon being clipped (a plane piece); a solid needs four faces.
    return out if len(solid) == 1 or len(out) >= 4 else []


def key(p):
    return tuple(round(c, 5) for c in p)


def world_box(e):
    if "_wb" not in e:
        lo, hi = box(e)
        pts = [to_world(e, (x, y, z)) for x in (lo[0], hi[0]) for y in (lo[1], hi[1]) for z in (lo[2], hi[2])]
        e["_wb"] = [min(p[i] for p in pts) for i in range(3)], [max(p[i] for p in pts) for i in range(3)]
    return e["_wb"]


def planes_in(e, other):
    """The six face planes of `other` as (point, outward unit normal) in e's coordinates."""
    lo, hi = box(other)
    planes = []
    for i in range(3):
        for side, n in ((lo, -1), (hi, 1)):
            p = list(lo)
            p[i] = side[i]
            q = list(p)
            q[i] += n
            a, b = to_local(e, to_world(other, p)), to_local(e, to_world(other, q))
            planes.append((a, v_unit(v_sub(b, a))))
    return planes


def solid_planes(e, other):
    """Face planes of other's current solid as (point, outward unit normal) in e's coordinates."""
    planes = []
    for f in other["solid"]:
        p0 = to_local(e, to_world(other, f["pts"][0]))
        q = to_local(e, to_world(other, v_add(f["pts"][0], f["n"])))
        planes.append((p0, v_unit(v_sub(q, p0))))
    return planes


def overlap_volume(a, b):
    inside = a["solid"]
    for p0, n in solid_planes(a, b):
        inside = clip(inside, p0, n)
        if not inside:
            return 0
    return volume(inside)


def flat_axes(e, thin):
    return [i for i, n in enumerate(size_of(e)) if n < thin]


def boxes_touch(a, b):
    alo, ahi = world_box(a)
    blo, bhi = world_box(b)
    return all(alo[i] < bhi[i] and blo[i] < ahi[i] for i in range(3))


def resolve_overlaps(elements, thin=EPS):
    """Cut cubes apart wherever they sink into each other, so paper pieces never collide.

    Every overlapping pair is separated by one face plane of one of the two pieces: the
    other piece keeps only what lies outside that plane. Of all such cuts the one that
    takes away the least material wins. Pieces only shrink, so a separated pair stays
    separated. Sets e["cut"] = (pieces e is glued to, e's solid) on cut solids and
    e["plane_cut"] = (kept polygon, glue edge) on cut planes (cubes thinner than `thin`).
    Returns buried pieces.
    """
    solids = [e for e in elements if not flat_axes(e, thin)]
    for e in elements:
        e.pop("cut", None)
        e.pop("plane_cut", None)
    for e in solids:
        e["solid"], e["glued"] = box_solid(e), []
        e["full"] = volume(e["solid"])
    pairs = []
    for i, a in enumerate(solids):
        for b in solids[i + 1:]:
            if boxes_touch(a, b):
                v = overlap_volume(a, b)
                if v > 1e-4 * min(a["full"], b["full"]):
                    pairs.append((v, a, b))
    buried = set()
    for _, a, b in sorted(pairs, key=lambda t: -t[0]):
        if id(a) in buried or id(b) in buried:
            continue
        if overlap_volume(a, b) <= 1e-4 * min(a["full"], b["full"]):
            continue  # an earlier cut already took the shared part away
        best = None
        for cut, by in ((a, b), (b, a)):
            before = volume(cut["solid"])
            for p0, n in solid_planes(cut, by):
                kept = clip(cut["solid"], p0, v_mul(n, -1))
                removed = before - (volume(kept) if kept else 0)
                if best is None or removed < best[0]:
                    best = (removed, cut, by, kept)
        _, cut, by, kept = best
        if not kept or volume(kept) < 0.02 * cut["full"]:
            buried.add(id(cut))
            continue
        cut["solid"] = kept
        cut["glued"].append(by)
    for e in solids:
        e["cut"] = (e["glued"], e["solid"]) if e["glued"] else None

    for e in elements:
        axes = flat_axes(e, thin)
        if len(axes) != 1:
            continue
        first = PLANE_FACES[axes[0]][0]
        tl, tr, bl = face_corners(e)[first]
        face = [{"pts": [tl, tr, v_add(tr, v_sub(bl, tl)), bl], "n": NORMALS[first], "src": first}]
        full = poly_area(face[0]["pts"], face[0]["n"])
        glue = None
        for s in solids:
            if id(s) in buried or not boxes_touch(e, s):
                continue
            planes_s = solid_planes(e, s)
            inside = face
            for p0, n in planes_s:
                inside = clip(inside, p0, n)
            if not inside or poly_area(inside[0]["pts"], face[0]["n"]) < 1e-4 * full:
                continue
            p0, n, kept = max(((p0, n, clip(face, p0, v_mul(n, -1))) for p0, n in planes_s),
                              key=lambda c: poly_area(c[2][0]["pts"], face[0]["n"]) if c[2] else 0)
            if not kept or poly_area(kept[0]["pts"], face[0]["n"]) < 0.05 * full:
                buried.add(id(e))
                break
            face = kept[:1]
            edge = [q for q in face[0]["pts"] if abs(v_dot(n, v_sub(q, p0))) < 1e-6]
            if len(edge) >= 2:
                glue = (edge[0], edge[-1])
        if id(e) not in buried and face[0]["pts"] != [tl, tr, v_add(tr, v_sub(bl, tl)), bl]:
            e["plane_cut"] = (face[0]["pts"], glue)
    return [e for e in elements if id(e) in buried]


def unfold(solid):
    """Lay the faces of a convex solid flat without overlaps.

    Returns (placed, folds): placed[i] = 2D points of face i (same order as its 3D
    points), folds = list of (face a, face b, 3D edge keys) that stay connected.
    """
    edges = {}
    for i, f in enumerate(solid):
        pts = f["pts"]
        for a, b in zip(pts, pts[1:] + pts[:1]):
            edges.setdefault(frozenset((key(a), key(b))), []).append(i)
    neighbours = {i: [] for i in range(len(solid))}
    for k, fs in edges.items():
        if len(fs) == 2:
            a, b = fs
            length = math.dist(*k) if len(k) == 2 else 0
            neighbours[a].append((length, b, k))
            neighbours[b].append((length, a, k))

    def flat(f):
        """Face points in its own plane, seen from outside, y pointing down."""
        pts, n = f["pts"], f["n"]
        u = v_unit(v_sub(pts[1], pts[0]))
        if f["src"]:
            tl, tr, _ = f["tex"]
            u = v_unit(v_sub(tr, tl))
        down = v_cross(u, n)
        return [(v_dot(v_sub(p, pts[0]), u), v_dot(v_sub(p, pts[0]), down)) for p in pts]

    local = [flat(f) for f in solid]
    roots = sorted(range(len(solid)), key=lambda i: (solid[i]["src"] is None, -poly_area(solid[i]["pts"], solid[i]["n"])))
    best = None
    for root in roots:
        placed, folds, queue = {root: local[root]}, [], [root]
        while queue:
            i = queue.pop(0)
            for _, j, k in sorted(neighbours[i], key=lambda t: -t[0]):
                if j in placed:
                    continue
                ka, kb = tuple(k)
                ia = [key(p) for p in solid[i]["pts"]]
                ja = [key(p) for p in solid[j]["pts"]]
                a2, b2 = placed[i][ia.index(ka)], placed[i][ia.index(kb)]
                a1, b1 = local[j][ja.index(ka)], local[j][ja.index(kb)]
                ang = math.atan2(b2[1] - a2[1], b2[0] - a2[0]) - math.atan2(b1[1] - a1[1], b1[0] - a1[0])
                c, s = math.cos(ang), math.sin(ang)
                placed[j] = [(a2[0] + c * (x - a1[0]) - s * (y - a1[1]), a2[1] + s * (x - a1[0]) + c * (y - a1[1]))
                             for x, y in local[j]]
                folds.append((i, j, k))
                queue.append(j)
        if len(placed) < len(solid):
            continue
        polys = [placed[i] for i in range(len(solid))]
        if any(overlap(polys[a], polys[b]) for a in range(len(polys)) for b in range(a)):
            continue
        xs = [p[0] for poly in polys for p in poly]
        ys = [p[1] for poly in polys for p in poly]
        area = (max(xs) - min(xs)) * (max(ys) - min(ys))
        if best is None or area < best[0]:
            best = (area, polys, folds)
    return best and best[1:]


def overlap(p, q, eps=1e-4):
    """Do two convex polygons overlap by more than a touch?"""
    for poly in (p, q):
        for (x0, y0), (x1, y1) in zip(poly, poly[1:] + poly[:1]):
            nx, ny = y0 - y1, x1 - x0
            n = math.hypot(nx, ny)
            if n < 1e-12:
                continue
            a = [(x * nx + y * ny) / n for x, y in p]
            b = [(x * nx + y * ny) / n for x, y in q]
            if max(a) <= min(b) + eps or max(b) <= min(a) + eps:
                return False
    return True


def slanted_piece(e, solid, textures, unit, px_mm, dpi, label):
    result = unfold(solid)
    if not result:
        return None
    polys, folds = result
    polys = [[(x * unit, y * unit) for x, y in poly] for poly in polys]
    fold_edges = {k for _, _, k in folds}

    # One glue flap per edge that is cut apart, on whichever side has room for it.
    flaps, cuts, flap_folds = [], [], []
    seen = set()
    for i, f in enumerate(solid):
        pts = f["pts"]
        for s in range(len(pts)):
            k = frozenset((key(pts[s]), key(pts[(s + 1) % len(pts)])))
            if k in fold_edges or len(k) < 2:
                continue
            a2, b2 = polys[i][s], polys[i][(s + 1) % len(pts)]
            if k in seen:
                cuts.append((a2, b2))
                continue
            poly = polys[i]
            cx, cy = sum(p[0] for p in poly) / len(poly), sum(p[1] for p in poly) / len(poly)
            length = math.dist(a2, b2)
            nx, ny = (b2[1] - a2[1]) / length, -(b2[0] - a2[0]) / length
            if nx * (a2[0] - cx) + ny * (a2[1] - cy) < 0:
                nx, ny = -nx, -ny
            placed = False
            for depth in (3, 1.8):
                depth = min(depth, length * 0.8)
                inset = min(depth, length * 0.35)
                dx, dy = (b2[0] - a2[0]) / length, (b2[1] - a2[1]) / length
                tab = [a2, (a2[0] + dx * inset + nx * depth, a2[1] + dy * inset + ny * depth),
                       (b2[0] - dx * inset + nx * depth, b2[1] - dy * inset + ny * depth), b2]
                if not any(overlap(tab, other) for other in polys + flaps):
                    flaps.append(tab)
                    flap_folds.append((a2, b2))
                    seen.add(k)
                    placed = True
                    break
            if not placed:
                cuts.append((a2, b2))

    points = [p for poly in polys + flaps for p in poly]
    sheet = Sheet(bounds(points), px_mm, dpi, label)
    for tab in flaps:
        sheet.tab(tab)
    for f, poly in zip(solid, polys):
        paint_face(sheet, e, f, poly, textures, unit)
    for i, j, k in folds:
        ka, kb = tuple(k)
        ia = [key(p) for p in solid[i]["pts"]]
        sheet.fold(polys[i][ia.index(ka)], polys[i][ia.index(kb)])
    for a, b in flap_folds:
        sheet.fold(a, b)
    for a, b in cuts:
        sheet.cut(a, b)
    return [sheet.img]


def paint_face(sheet, e, f, poly, textures, unit):
    pix = [sheet.pt(p) for p in poly]
    X0, Y0 = int(min(p[0] for p in pix)), int(min(p[1] for p in pix))
    X1, Y1 = int(max(p[0] for p in pix)) + 2, int(max(p[1] for p in pix)) + 2
    mask = Image.new("L", (X1 - X0, Y1 - Y0), 0)
    ImageDraw.Draw(mask).polygon([(x - X0, y - Y0) for x, y in pix], fill=255)
    tile = Image.new("RGBA", mask.size, "white")
    img = None
    if f["src"]:
        fw, fh = face_dims(e)[f["src"]]
        W, H = max(1, round(fw * unit * sheet.k)), max(1, round(fh * unit * sheet.k))
        img = face_image(textures, e["faces"].get(f["src"]), W, H)
    if img is not None:
        tl, tr, bl = f["tex"]
        ux, vy = v_sub(tr, tl), v_sub(bl, tl)
        st = [(v_dot(v_sub(p, tl), ux) / v_dot(ux, ux) * W, v_dot(v_sub(p, tl), vy) / v_dot(vy, vy) * H)
              for p in f["pts"]]
        coeffs = affine([(x - X0, y - Y0) for x, y in pix], st)
        if coeffs:
            tile.alpha_composite(img.transform(mask.size, Image.Transform.AFFINE, coeffs, Image.NEAREST))
    elif not f["src"]:
        # The slanted glue face: hatched, it goes flat onto the neighbouring piece.
        d = ImageDraw.Draw(tile)
        step = max(4, round(1.5 * sheet.k))
        for x in range(-tile.height, tile.width, step):
            d.line([(x, tile.height), (x + tile.height, 0)], fill=(200, 200, 200, 255), width=1)
    sheet.img.paste(tile, (X0, Y0), mask)


def affine(src, dst):
    """Coefficients of the affine map taking src points to dst points (first 3 non-collinear)."""
    for i in range(len(src)):
        for j in range(i + 1, len(src)):
            for k in range(j + 1, len(src)):
                (x0, y0), (x1, y1), (x2, y2) = src[i], src[j], src[k]
                det = (x1 - x0) * (y2 - y0) - (x2 - x0) * (y1 - y0)
                if abs(det) < 1e-6:
                    continue
                out = []
                for c in range(2):
                    d0, d1, d2 = dst[i][c], dst[j][c], dst[k][c]
                    a = ((d1 - d0) * (y2 - y0) - (d2 - d0) * (y1 - y0)) / det
                    b = ((x1 - x0) * (d2 - d0) - (x2 - x0) * (d1 - d0)) / det
                    out += [a, b, d0 - a * x0 - b * y0]
                return tuple(out)
    return None


# ---------------------------------------------------------------- pages

def axes(e):
    o = to_world(e, (0, 0, 0))
    return [v_sub(to_world(e, axis), o) for axis in ((1, 0, 0), (0, 1, 0), (0, 0, 1))]


def tilt(e, other=None):
    """Angle between the cube and another cube (or the model itself), degrees."""
    a = axes(e)
    b = axes(other) if other else [(1, 0, 0), (0, 1, 0), (0, 0, 1)]
    trace = sum(v_dot(x, y) for x, y in zip(a, b))
    return math.degrees(math.acos(max(-1, min(1, (trace - 1) / 2))))


def base_of(e, others, tol=0.05):
    """The touching cube e is turned least against: what e sits on when assembled."""
    elo, ehi = world_box(e)
    touching = [o for o in others if o is not e
                and all(world_box(o)[0][i] <= ehi[i] + tol and elo[i] <= world_box(o)[1][i] + tol for i in range(3))]
    return min(touching, key=lambda o: tilt(e, o), default=None)


def piece_note(e, numbers):
    """(к N): glue the hatched face onto piece N; (N° к M): the piece is turned by N
    degrees against piece M it sits on."""
    if e.get("over"):
        return f"(поверх {numbers[id(e['over'])]}) " if id(e["over"]) in numbers else ""
    if e.get("plane_cut") and e.get("base"):
        return f"(к {numbers[id(e['base'])]}) "
    if e.get("cut"):
        return f"(к {', '.join(str(numbers[id(o)]) for o in e['cut'][0] if id(o) in numbers)}) "
    base = e.get("base")
    angle = tilt(e, base) if base else tilt(e)
    if angle < 3:
        return ""
    return f"({angle:.0f}° к {numbers[id(base)]}) " if base else f"({angle:.0f}°) "


def piece_name(e):
    """Group path without the root group every model has, plus the size in model units."""
    path = [g["name"] for g in e["groups"]]
    if len(path) > 1:
        path = path[1:]
    if len(path) > 2:
        path = [path[0], "…", path[-1]]
    lo, hi = box(e)
    dims = "×".join(f"{b - a:g}" for a, b in zip(lo, hi) if b - a > EPS)
    return f"{'/'.join(path) or e.get('name', '')}  {dims}"


def legend_page(elements, numbers, textures, dpi, credit):
    """Assembly reference: the model from the front and from the back, pieces numbered."""
    page_w, page_h, margin = page_geometry(dpi)
    page = Image.new("RGB", (page_w, page_h), "white")
    draw = ImageDraw.Draw(page)
    mm = dpi / 25.4
    font = load_font(round(3 * mm))
    small = load_font(round(2.4 * mm))
    if credit:
        draw.text((margin, margin // 2), credit, fill="black", font=font, anchor="lm")
    draw.text((margin, margin + round(2 * mm)), "Сборка", fill="black", font=load_font(round(5 * mm)), anchor="lm")

    size = (page_w - 2 * margin - round(6 * mm)) // 2
    top = margin + round(7 * mm)
    views = []
    for col, yaw in enumerate((30, 210)):
        img, owner = render_preview(elements, textures, size, yaw=yaw)
        x0 = margin + col * (size + round(6 * mm))
        page.paste(img, (x0, top))
        views.append((img, owner, x0))

    # Put every number on the view where most of that piece is seen, at the middle of
    # what is seen; pieces hidden in both views are skipped.
    # Per view and piece: pixels seen, their x and y sums, and every 7th pixel as a
    # candidate spot for the mark.
    seen = [[[0, 0, 0, []] for _ in elements] for _ in views]
    for v, (_, owner, _) in enumerate(views):
        for k, index in enumerate(owner):
            if index >= 0:
                st = seen[v][index]
                if st[0] % 7 == 0:
                    st[3].append(k)
                st[0] += 1
                st[1] += k % size
                st[2] += k // size
    r = round(2.2 * mm)
    marks = []
    for i, e in enumerate(elements):
        v = max(range(len(views)), key=lambda v: seen[v][i][0])
        count, sx, sy, pixels = seen[v][i]
        if not count:
            continue
        # The seen pixel closest to their mean keeps the mark on the piece for L-shapes;
        # pixels where the mark would cover another mark are tried last.
        mx, my = sx / count, sy / count
        clash = lambda k: sum(max(0, 2 * r - math.hypot(views[v][2] + k % size - X, top + k // size - Y))
                              for X, Y in marks)
        k = min(pixels, key=lambda k: (clash(k) * 50) ** 2 + (k % size - mx) ** 2 + (k // size - my) ** 2)
        X, Y = views[v][2] + k % size, top + k // size
        marks.append((X, Y))
        draw.ellipse((X - r, Y - r, X + r, Y + r), fill="white", outline="black", width=max(2, round(0.2 * mm)))
        draw.text((X, Y), str(numbers[id(e)]), fill="black", font=small, anchor="mm")

    y = top + size + round(6 * mm)
    notes = ("Сплошные линии — резать, пунктир — сгибать, белые клапаны — клеить. "
             "Плоские детали сгибаются пополам и склеиваются изнанкой; вырезные (волоски и т.п.) "
             "печатаются лицом и изнанкой: склейте их спинками, клапаны разведите в стороны "
             "и приклейте к фигурке. Заштрихованная грань — скошенный срез: в списке «(к N)», "
             "приклейте её к детали N, и деталь сама встанет под нужным углом. «(N° к M)» — деталь "
             "повёрнута на столько градусов относительно детали M, наклон смотрите на картинке. "
             "«(поверх N)» — внешний слой: вырежьте его грани по контуру (подписаны перед, зад, лево, право, "
             "верх, низ) и наклейте поверх граней детали N.")
    words, line = notes.split(), ""
    for w in words:
        if draw.textlength(line + " " + w, font=small) > page_w - 2 * margin:
            draw.text((margin, y), line, fill="black", font=small, anchor="lt")
            y, line = y + round(3.6 * mm), w
        else:
            line = (line + " " + w).strip()
    draw.text((margin, y), line, fill="black", font=small, anchor="lt")

    # Piece list in three columns, continued on more pages when long.
    pages = [page]
    col_w = (page_w - 2 * margin) // 3
    step = round(4 * mm)
    y += round(8 * mm)
    rows = (page_h - margin - y) // step
    col = row = 0
    for e in elements:
        if row == rows:
            col, row = col + 1, 0
        if col == 3:
            page = Image.new("RGB", (page_w, page_h), "white")
            draw = ImageDraw.Draw(page)
            if credit:
                draw.text((margin, margin // 2), credit, fill="black", font=font, anchor="lm")
            pages.append(page)
            y, col, row = margin, 0, 0
            rows = (page_h - 2 * margin) // step
        text = f"{numbers[id(e)]}. {piece_note(e, numbers)}{piece_name(e)}"
        while draw.textlength(text, font=small) > col_w - round(2 * mm):
            text = text[:-2] + "…"
        draw.text((margin + col * col_w, y + row * step), text, fill="black", font=small, anchor="lt")
        row += 1
    return pages


# Cubes thinner than this on paper become flat pieces, mm.
THIN_MM = 0.5
# Room taken by glue flaps, labels and padding around a piece, mm.
PIECE_EXTRA_MM = 12


def size_of(e):
    lo, hi = box(e)
    return [b - a for a, b in zip(lo, hi)]


def net_extent(e):
    """Width and height of an element's piece in model units, without flaps."""
    w, h, d = size_of(e)
    flat = [i for i, n in enumerate((w, h, d)) if n < EPS]
    if not flat:
        return 2 * (w + d), h + 2 * d
    if flat[0] == 1:
        return w, 2 * d
    return 2 * (w if flat[0] == 2 else d), h


def fit_unit(extent, limit):
    """Biggest unit (mm) that fits the extent on the page, either way round."""
    a, b = (max(n, EPS) for n in extent)
    lx, ly = (n - PIECE_EXTRA_MM for n in limit)
    return max(min(lx / a, ly / b), min(lx / b, ly / a))


def render(path, unit_mm, dpi, credit, emit=None):
    """Pages of the papercraft and the scale used. With `emit`, every page is handed to it
    as soon as it is done instead of being returned: an A4 page at 300 dpi is 26 MB."""
    pages = []
    emit = emit or pages.append
    elements, textures = load_model(path)
    kept = []
    for e in elements:
        if WATERMARK.search(e.get("name", "")):
            print(f"  skipped watermark {e.get('name')!r}", file=sys.stderr)
        elif e.get("visibility") is False or e.get("export") is False:
            continue
        else:
            kept.append(e)
    clear = {id(e) for e in kept if clear_cube(e, textures)}
    visible = [e for e in kept if not hidden_inside(e, kept, clear)]
    if len(visible) < len(kept):
        print(f"  skipped {len(kept) - len(visible)} cube(s) hidden inside others", file=sys.stderr)

    _, page_h, margin = page_geometry(dpi)
    page_w = page_geometry(dpi)[0]
    px_mm = dpi / 25.4
    # Shrink the model until its biggest piece fits on a page: first from the net sizes
    # (drawing a piece at a wrong scale can take metres of pixels), then exactly.
    limit = ((page_w - 2 * margin) / px_mm, (page_h - 2 * margin) / px_mm - 6)
    fit = min(fit_unit(net_extent(e), limit) for e in visible)
    if fit < unit_mm:
        unit_mm = max(0.05, int(fit * 20) / 20)
        print(f"  unit reduced to {unit_mm} mm to fit A4", file=sys.stderr)
    tiny = sum(1 for e in visible
               if min((d * unit_mm for d in size_of(e) if d * unit_mm >= THIN_MM), default=2) < 2)
    if tiny:
        hint = ("the biggest piece limits the scale, the model is too detailed for A4" if fit < unit_mm + 0.05
                else "try a bigger --unit-mm")
        print(f"  {tiny} piece(s) have edges under 2 mm, hard to cut: {hint}", file=sys.stderr)
    # See-through cubes around other cubes are outer layers: printed as cut-outs glued
    # over the cube inside, and left out of cutting cubes apart.
    for e in visible:
        if id(e) in clear:
            e["over"] = wrapped(e, visible)
    whole = visible
    while True:
        # Cubes thinner than paper become planes, which depends on the scale.
        buried = resolve_overlaps([e for e in whole if not e.get("over")], THIN_MM / unit_mm)
        visible = [e for e in whole if all(e is not b for b in buried)]
        for e in visible:
            e["base"] = base_of(e, visible)
        numbers = {id(e): i for i, e in enumerate(visible, 1)}
        sprites = [s for e in visible for s in element_pieces(e, textures, unit_mm, px_mm, dpi, str(numbers[id(e)]))]
        worst = max(min(max(s.width / px_mm / limit[0], s.height / px_mm / limit[1]),
                        max(s.height / px_mm / limit[0], s.width / px_mm / limit[1])) for s in sprites)
        if worst <= 1:
            if buried:
                print(f"  skipped {len(buried)} cube(s) buried inside others", file=sys.stderr)
            break
        unit_mm = int(unit_mm / worst * 20) / 20
        print(f"  unit reduced to {unit_mm} mm to fit A4", file=sys.stderr)
    sprites.sort(key=lambda s: s.width * s.height, reverse=True)

    cell = max(4, round(px_mm))  # ~1 mm grid
    gap = 2
    credit_px = round(2.5 * px_mm)
    while sprites:
        page, taken = blank_page(dpi, credit, credit_px)
        occ, _, _ = cell_rows(taken, cell)
        del taken
        before = len(sprites)
        sprites = pack(page, occ, sprites, cell, gap)
        if len(sprites) == before:
            sys.exit("  a piece is bigger than a whole page")
        emit(page)
        del page
    for page in legend_page(visible, numbers, textures, dpi, credit):
        emit(page)
    return pages, unit_mm


def main():
    ap = argparse.ArgumentParser(description="Blockbench model -> papercraft net")
    ap.add_argument("models", nargs="+", help=".bbmodel files (textures must be embedded)")
    ap.add_argument("--credit", default="tg: @faustyu", help='text in the top corner of every page ("" = none)')
    ap.add_argument("--author", default="", help="model author, printed next to the credit")
    ap.add_argument("--unit-mm", type=float, default=8, help="size of one model unit (pixel) on paper, mm (default 8)")
    ap.add_argument("--dpi", type=int, default=300)
    ap.add_argument("--out-dir", type=Path, help="output folder (default: next to the model)")
    args = ap.parse_args()

    credit = " · ".join(filter(None, [args.credit, args.author and f"модель: {args.author}"]))
    for path in map(Path, args.models):
        out_dir = args.out_dir or path.parent
        out_dir.mkdir(parents=True, exist_ok=True)
        stem = out_dir / f"{path.stem}_papercraft"
        pdf, pngs = PdfWriter(args.dpi), []

        def emit(page):
            png = stem.with_suffix(".png") if not pngs else out_dir / f"{stem.name}_page{len(pngs) + 1}.png"
            page.save(png, dpi=(args.dpi, args.dpi), compress_level=3)
            pdf.add(page)
            pngs.append(png)

        _, unit = render(path, args.unit_mm, args.dpi, credit, emit)
        pdf.save(stem.with_suffix(".pdf"))
        files = ", ".join(p.name for p in pngs + [stem.with_suffix(".pdf")])
        print(f"{path.name}: unit {unit} mm, {len(pngs)} page(s) -> {files}")


if __name__ == "__main__":
    main()
