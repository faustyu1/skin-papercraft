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
import base64
import io
import json
import math
import re
import sys
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter

from skin_papercraft import (
    ALPHA_CUTOFF, build_net, cell_rows, dashed_line, load_font, pack, page_geometry, save_pdf,
)

# Texels per model unit when a face texture is resampled for print or preview.
SAMPLE = 16
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
        e["label"] = "/".join(g["name"] for g in chain) or e.get("name", "")
        elements.append(e)
    return data.get("name") or Path(path).stem, elements, textures


def box(e):
    inf = e.get("inflate") or 0
    lo = [a - inf for a in e["from"]]
    hi = [b + inf for b in e["to"]]
    return lo, hi


def is_rotated(obj):
    return any(abs(r) > EPS for r in obj.get("rotation") or (0, 0, 0))


def hidden_inside(e, others):
    """A cube fully enclosed by another cube of the same group is never seen."""
    lo, hi = box(e)
    for o in others:
        if o is e or o["groups"] != e["groups"] or is_rotated(o) or is_rotated(e):
            continue
        olo, ohi = box(o)
        if all(ol <= l + EPS and h <= oh + EPS for l, h, ol, oh in zip(lo, hi, olo, ohi)) and (lo, hi) != (olo, ohi):
            return True
    return False


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


def to_world(e, p):
    """Apply the cube rotation, then every parent group rotation, innermost first."""
    for obj in [e] + e["groups"][::-1]:
        if is_rotated(obj):
            p = apply(rot_matrix(obj["rotation"]), p, obj.get("origin", (0, 0, 0)))
    return p


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

    Faces are painted back to front. Returns (image, project) where project maps a
    world point to image pixels.
    """
    view = mat_mul(rot_matrix((pitch, 0, 0)), rot_matrix((0, yaw, 0)))
    # The camera looks from -Z (the front); +X is on the viewer's left.
    cam = lambda p: (lambda q: (-q[0], -q[1], q[2]))(apply(view, p))

    quads = []
    for e in elements:
        dims = face_dims(e)
        for name, corners in face_corners(e).items():
            fw, fh = dims[name]
            if fw < EPS or fh < EPS:
                continue
            img = face_image(textures, e["faces"].get(name), max(1, round(fw * SAMPLE)), max(1, round(fh * SAMPLE)))
            if img is not None:
                quads.append(([cam(to_world(e, c)) for c in corners], img))
    if not quads:
        sys.exit("model has no textured faces")

    xs = [p[0] for q, _ in quads for p in q] + [q[1][0] + q[2][0] - q[0][0] for q, _ in quads]
    ys = [p[1] for q, _ in quads for p in q] + [q[1][1] + q[2][1] - q[0][1] for q, _ in quads]
    scale = 0.92 * size / max(max(xs) - min(xs), max(ys) - min(ys))
    cx, cy = (max(xs) + min(xs)) / 2, (max(ys) + min(ys)) / 2
    screen = lambda p: (size / 2 + (p[0] - cx) * scale, size / 2 + (p[1] - cy) * scale)

    # Plain z-buffer: faces of thin decals sit a hair above other faces, painter's
    # sorting gets those wrong.
    color = [(255, 255, 255)] * (size * size)
    depth = [math.inf] * (size * size)
    for pts, img in quads:
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
                    color[k] = (int(r * shade), int(g * shade), int(b * shade))
    canvas = Image.new("RGB", (size, size))
    canvas.putdata(color)
    return canvas, lambda p: screen(cam(p))
