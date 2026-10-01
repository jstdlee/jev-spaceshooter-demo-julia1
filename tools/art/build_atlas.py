#!/usr/bin/env python3
"""Cut the raw art sheets into game sprites at the retro pixel scale and pack them into one atlas.

    python3 tools/art/build_atlas.py <rawarts-dir> <out-dir>     # writes atlas.png, atlas.json, preview.png

Every sprite is: cropped from its sheet (sheet pixels), cut out of its background, trimmed, rotated to its game
orientation, fitted into its target box in retro pixels (one retro pixel = 2 game pixels), and reduced to a small
palette without dithering. Ships get hard alpha edges and a dark outline; glows keep a few alpha steps.
"""
import json
import os
import sys

import numpy as np
from PIL import Image

# ---- sheets (file names in the raw art folder) ------------------------------------------------------------------
SHEET = {
    "roster": "Gemini_Generated_Image_ch1o7ich1o7ich1o.jpeg",      # Dart, Wasp, Crab, Mantis, Hornet, rhino mech, green hydra, turrets, FX
    "roster2": "Gemini_Generated_Image_s0f9d3s0f9d3s0f9.jpeg",     # purple Hydra, grey Mantis, shield barrier, massive explosion
    "gunship": "Gemini_Generated_Image_w4q22iw4q22iw4q2.jpeg",     # player gunship: idle, muzzle flash, burst, blue flame, sparks
    "shipboom": "Gemini_Generated_Image_ul88lful88lful88(1).jpeg", # 4x3 ship destruction sequence
    "barrels": "Gemini_Generated_Image_ul88lful88lful88.jpeg",     # cannon barrels by heat
    "orbs": "Gemini_Generated_Image_abtw03abtw03abtw.jpeg",        # blue plasma orbs (growing)
    "bolts_small": "Untitled.jpeg",                                # blue rounds with sparks
    "micromissile": "watermarked_img_10762713819258257763.jpg",    # micro missiles and exhaust puffs
    "shield_blue": "watermarked_img_12083979555593291509.jpg",     # blue shield bubble: idle hum + impact
    "capitals": "watermarked_img_12473384721902827343.jpg",        # six white capital ships (side view)
    "bolts_long": "watermarked_img_13204592083441560272.jpg",      # blue bolts with long trails
    "orbs2": "watermarked_img_13670135132706667408.jpg",           # blue orbs, second set
    "beam": "watermarked_img_14267818056944378944.jpg",            # magenta beam: charging head, mid segment, impact
    "rounds": "watermarked_img_14597151550400182749.jpg",          # blue rounds with growing trails
    "silverjets": "watermarked_img_15000675116776680483.jpg",      # six silver jets
    "barrels2": "watermarked_img_16421645691296860613.jpg",        # cannon barrels, second set
    "battleship": "watermarked_img_17081844013136637314.jpg",      # grey battleship (side view)
    "orbs3": "watermarked_img_18054891964759687505.jpg",           # blue comet orbs
    "weapons": "watermarked_img_1998601884957688931.jpg",          # 8-way missiles, red beam bolts, acid blobs
    "stealth": "watermarked_img_2096405922830663754.jpg",          # six dark stealth fighters
    "alienship": "watermarked_img_400316883171196282.jpg",         # dark alien ship with green glow
    "boom": "watermarked_img_6914943601176163513.jpg",             # 8-frame explosion
    "gunship2": "watermarked_img_7134174462382047738.jpg",         # player gunship, second set
    "acid": "watermarked_img_7944264953374740134.jpg",             # green plasma balls
    "shield_hex": "watermarked_img_8329694814951213107.jpg",       # hex shield bubble, 8 frames
    "roster_a": "Gemini_Generated_Image_8iko8r8iko8r8iko.jpeg",    # roster, first draft
    "roster_b": "watermarked_img_831962976434258846.jpg",          # roster, third draft
    "roster_c": "watermarked_img_9882961287316827006.jpg",         # roster, fourth draft
    "shipboom2": "watermarked_img_13333508292485498225.jpg",       # ship destruction, second set
}

# ---- sprites ------------------------------------------------------------------------------------------------------
# name: (sheet, (x0, y0, x1, y1), mode, rotate_deg counter-clockwise, (max_w, max_h), colors, outline)
#   mode: "flat" (key out the crop's border colour) or "glow" (black background, alpha from brightness)
S = {}


# Rim colours: a light, team-coloured outline makes small art pop on the dark starfield and tells friend from foe.
RIM = {"ally": (0, 216, 255), "enemy": (255, 64, 96), "boss": (255, 63, 210), "item": (255, 226, 61)}
RIMS = False  # team-coloured outlines (off: they looked like a shiny edge)
SCALE = 1.2   # on-screen size relative to the hitbox-sized boxes below (the hitboxes themselves do not change)


def add(name, sheet, box, mode, rot, size, colors=14, outline=False, inset=8):
    team = None
    if outline:
        team = "boss" if name.startswith("boss") else "ally" if name.startswith(("player", "jet", "gun_pod", "capsules")) else "enemy"
    if mode == "flat" and outline:
        size = (round(size[0] * SCALE), round(size[1] * SCALE))
    # No rim: the coloured outlines read as a shiny edge. The vivid colours carry the contrast instead.
    S[name] = (sheet, box, mode, rot, size, colors, team if RIMS else None, inset)


# Player gunship (nose up, as in the game) and its firing frames.
for i, box in enumerate([(78, 366, 558, 1152), (606, 276, 1086, 1152), (1134, 126, 1626, 1194), (1668, 264, 2154, 1152), (2196, 366, 2682, 1152)]):
    add(f"player_{i}", "gunship", box, "flat", 0, (16, 24), 14, True, 4)
for i, box in enumerate([(36, 180, 282, 576), (300, 138, 546, 576), (564, 60, 816, 594), (834, 126, 1080, 582), (1098, 162, 1344, 576)]):
    add(f"player2_{i}", "gunship2", box, "flat", 0, (16, 24), 14, True, 4)

# Escort jets: silver fighters (nose up).
for i, box in enumerate([(72, 42, 402, 348), (510, 36, 924, 354), (1002, 42, 1368, 348), (24, 414, 444, 738), (528, 408, 912, 750), (1008, 396, 1362, 756)]):
    add(f"jet_{i}", "silverjets", box, "flat", 0, (11, 11), 10, True, 4)

# Enemies face down (rotated 180).
for i, box in enumerate([(18, 36, 336, 354), (498, 12, 936, 366), (996, 48, 1374, 360), (24, 414, 444, 744), (534, 402, 912, 750), (1008, 396, 1362, 756)]):
    add(f"stealth_{i}", "stealth", box, "flat", 180, (17, 14), 10, True, 4)
add("dart", "roster", (66, 162, 408, 486), "flat", 180, (16, 13), 12, True)
add("wasp", "roster", (552, 162, 840, 414), "flat", 180, (13, 11), 10, True)
add("hornet", "roster", (600, 500, 770, 630), "flat", 180, (13, 11), 10, True, 2)
add("crab", "roster", (996, 162, 1428, 474), "flat", 180, (25, 18), 12, True)
add("turret_twin", "roster", (1476, 900, 2010, 1116), "flat", -90, (25, 18), 10, True)
add("turret_base", "roster", (2070, 816, 2412, 1074), "flat", 180, (25, 18), 10, True)

# Bosses (about the 48x29 retro boss box): rotate the upright ones to face down; side-view ships stay horizontal.
add("boss_mantis", "roster", (1506, 162, 2010, 744), "flat", 180, (52, 34), 16, True)
add("boss_hydra", "roster2", (70, 650, 758, 1052), "flat", 180, (54, 32), 16, True)
add("boss_mantis_grey", "roster2", (816, 640, 1416, 1038), "flat", 180, (52, 34), 14, True)
add("boss_hydra_green", "roster", (816, 640, 1416, 1038), "flat", 180, (52, 34), 14, True)
add("boss_rhino", "roster", (70, 650, 776, 1052), "flat", 0, (54, 32), 14, True)
add("boss_battleship", "battleship", (12, 120, 1392, 648), "flat", 0, (62, 28), 16, True, 2)
add("boss_alien", "alienship", (48, 42, 1368, 720), "flat", 0, (62, 32), 14, True, 2)
for i, box in enumerate([(42, 12, 666, 240), (744, 42, 1356, 234), (42, 264, 666, 504), (744, 264, 1350, 504), (36, 516, 654, 750), (744, 522, 1338, 744)]):
    add(f"boss_capital_{i}", "capitals", box, "flat", 0, (60, 24), 12, True, 2)

# Pickups and boss big bomb.
add("shield_pickup", "roster", (2064, 228, 2364, 528), "flat", 0, (13, 13), 10, False)
add("big_bomb", "roster", (2436, 228, 2730, 528), "flat", 0, (14, 14), 10, False)
add("gun_pod", "roster", (70, 1220, 326, 1466), "flat", 0, (12, 12), 8, True)
add("gun_pod_b", "roster", (329, 1220, 586, 1466), "flat", 0, (12, 12), 8, True)
add("capsules", "roster", (2482, 818, 2746, 1074), "flat", 0, (12, 12), 8, True)

# Player shots, pointing up (sheets point right: rotate 90).
for i, box in enumerate([(102, 114, 228, 186), (366, 96, 552, 204), (660, 96, 876, 204), (972, 78, 1350, 216)]):
    add(f"round_{i}", "rounds", box, "glow", 90, (4, 9 + 2 * i), 8, False, 0)
add("needle", "bolts_long", (42, 42, 402, 198), "glow", 90, (3, 12), 6, False, 0)
add("bolt_small", "bolts_small", (270, 66, 402, 150), "glow", 90, (3, 6), 6, False, 0)
for i, box in enumerate([(168, 228, 462, 378), (690, 210, 1104, 402), (1338, 186, 1806, 426)]):
    add(f"orb_{i}", "orbs", box, "glow", 90, (6, 8 + 2 * i), 8, False, 0)
add("comet", "orbs3", (780, 192, 1362, 402), "glow", 90, (5, 12), 8, False, 0)
add("orb2", "orbs2", (342, 102, 552, 198), "glow", 90, (5, 8), 8, False, 0)

# Missiles: player micro missiles (sheet points right: rotate 90 to point up); enemy missile (points up).
add("missile_kinetic", "micromissile", (12, 66, 198, 108), "flat", 90, (4, 11), 8, False, 2)
add("missile_cluster", "micromissile", (12, 456, 198, 504), "flat", 90, (4, 11), 8, False, 2)
add("missile_emp", "micromissile", (12, 276, 198, 324), "flat", 90, (4, 11), 8, False, 2)
add("smoke_puff", "micromissile", (864, 108, 906, 168), "flat", 0, (5, 6), 6, False, 2)
add("enemy_missile", "weapons", (402, 24, 432, 102), "glow", 0, (5, 11), 8, False, 0)
add("rocket_blue", "roster", (704, 1185, 852, 1325), "flat", 45, (5, 12), 8, False, 2)
add("rocket_green", "roster", (852, 1185, 1000, 1325), "flat", 45, (5, 12), 8, False, 2)
add("fire_bolt", "roster", (704, 1325, 1000, 1466), "flat", 90, (5, 12), 8, False, 2)

# Enemy fire: green plasma (aimed), acid blobs (spray, animated), red beam bolts (fast, pointing down).
for i, box in enumerate([(48, 600, 156, 714), (204, 588, 336, 720), (372, 588, 510, 726), (552, 588, 678, 726), (726, 588, 858, 726)]):
    add(f"acid_{i}", "weapons", box, "glow", 0, (7, 7), 8, False, 0)
for i, box in enumerate([(36, 390, 132, 474), (156, 390, 252, 504), (276, 390, 366, 528), (402, 390, 486, 534)]):
    add(f"redbolt_{i}", "weapons", box, "glow", 180, (5, 10), 8, False, 0)
add("plasma_green", "acid", (90, 114, 312, 282), "flat", 0, (7, 7), 8, False, 2)
add("plasma_green_b", "acid", (474, 84, 792, 318), "flat", 0, (8, 8), 8, False, 2)

# Effects.
for i in range(8):
    col, row = i % 4, i // 4
    boxes = [(96, 42, 258, 210), (426, 30, 624, 222), (792, 36, 960, 222), (1080, 0, 1368, 258), (72, 288, 270, 492), (402, 264, 636, 504), (768, 300, 966, 480), (1104, 270, 1344, 474)]
    add(f"boom_{i}", "boom", boxes[i], "glow", 0, (20, 20), 10, False, 0)
for i in range(12):
    col, row = i % 4, i // 4
    add(f"shipboom_{i}", "shipboom", (col * 687, row * 512, col * 687 + 687, row * 512 + 512), "glow", 0, (60, 44), 14, False, 14)
add("massive_explosion", "roster2", (2070, 816, 2748, 1470), "flat", 0, (40, 40), 12, False)
add("shield_barrier", "roster2", (1476, 900, 2010, 1470), "flat", 0, (30, 30), 8, False)
add("plasma_blast_0", "roster", (1478, 1220, 1734, 1466), "flat", 0, (22, 22), 8, False)
add("plasma_blast_1", "roster", (1734, 1220, 1989, 1466), "flat", 0, (22, 22), 8, False)
add("homing_spread", "roster", (2006, 1220, 2253, 1466), "flat", 0, (16, 16), 8, False)
add("impact_ripple", "roster", (2274, 1220, 2513, 1466), "flat", 0, (14, 14), 6, False)
add("afterburner", "roster", (2541, 1220, 2795, 1466), "flat", 180, (5, 9), 6, False)
for i, box in enumerate([(24, 30, 336, 336), (366, 30, 672, 336), (702, 30, 1014, 336), (1044, 30, 1350, 336), (24, 384, 354, 714), (366, 408, 672, 714), (702, 408, 1014, 720), (1044, 408, 1350, 714)]):
    add(f"shield_blue_{i}", "shield_blue", box, "glow", 0, (32, 32), 8, False, 0)
for i, box in enumerate([(18, 54, 330, 366), (360, 54, 672, 366), (702, 54, 1014, 366), (1044, 54, 1356, 366), (18, 426, 330, 738), (360, 426, 672, 738), (702, 426, 1014, 738), (1044, 426, 1356, 738)]):
    add(f"shield_hex_{i}", "shield_hex", box, "glow", 0, (40, 40), 8, False, 0)
for i, box in enumerate([(42, 96, 204, 252), (222, 96, 384, 282), (408, 96, 564, 342), (588, 96, 762, 408)]):
    add(f"beamhead_{i}", "beam", box, "glow", 0, (14, 22), 10, False, 0)
add("beam_mid", "beam", (828, 96, 1074, 384), "glow", 90, (24, 12), 10, False, 0)
for i, box in enumerate([(54, 516, 264, 726), (276, 540, 438, 702), (456, 534, 630, 708), (660, 570, 762, 672)]):
    add(f"beam_impact_{i}", "beam", box, "glow", 0, (16, 16), 10, False, 0)
# Weapon heat icon (cannon barrels by heat: cold, red, orange, firing).
for i, box in enumerate([(132, 168, 1260, 438), (1392, 168, 2550, 438), (132, 834, 1260, 1098), (1392, 834, 2688, 1386)]):
    add(f"barrel_{i}", "barrels", box, "flat", 0, (26, 7), 10, False, 4)
for i, box in enumerate([(66, 84, 630, 360), (696, 84, 1278, 360), (66, 414, 630, 552), (696, 402, 1338, 702)]):
    add(f"barrel2_{i}", "barrels2", box, "flat", 0, (26, 7), 10, False, 4)
# The other roster drafts: alternate looks for the Dart, Wasp and Crab.
for tag, sheet in (("a", "roster_a"), ("b", "roster_b"), ("c", "roster_c")):
    scale = 1.0 if sheet != "roster_a" else 1.0
    add(f"dart_{tag}", sheet, (0, 0, 0, 0), "auto-roster", 180, (16, 13), 12, True)
# Second ship-destruction set: frames 0, 4 and 8 as variety.
for i in (0, 4, 8):
    col, row = i % 4, i // 4
    add(f"shipboom2_{i}", "shipboom2", (col * 344, row * 256, col * 344 + 344, row * 256 + 256), "glow", 0, (52, 38), 12, False, 8)


# ---- processing ---------------------------------------------------------------------------------------------------
def cut(img, mode):
    rgb = np.asarray(img.convert("RGB")).astype(np.float32)
    if mode == "glow":
        lum = rgb.max(axis=2)
        alpha = np.clip((lum - 18) / 90, 0, 1)
        color = np.clip(rgb / np.maximum(alpha[..., None], 1e-3), 0, 255) * (alpha[..., None] > 0)
        color = np.where(alpha[..., None] > 0, np.clip(rgb * 1.15, 0, 255), 0)
        return color, alpha
    border = np.concatenate([rgb[0], rgb[-1], rgb[:, 0], rgb[:, -1]])
    bg = np.median(border, axis=0)
    dist = np.sqrt(((rgb - bg) ** 2).sum(axis=2))
    alpha = np.clip((dist - 22) / 30, 0, 1)
    return rgb, alpha


def trim(color, alpha, thresh=0.25):
    ys, xs = np.nonzero(alpha > thresh)
    if not len(xs):
        return color, alpha
    return color[ys.min():ys.max() + 1, xs.min():xs.max() + 1], alpha[ys.min():ys.max() + 1, xs.min():xs.max() + 1]


def to_image(color, alpha):
    a = (alpha * 255).astype(np.uint8)
    return Image.fromarray(np.dstack([color.astype(np.uint8), a]), "RGBA")


def fit(img, size):
    w, h = img.size
    scale = min(size[0] / w, size[1] / h)
    nw, nh = max(1, round(w * scale)), max(1, round(h * scale))
    # Premultiplied downscale so the background never bleeds into the edge colours.
    arr = np.asarray(img).astype(np.float32)
    pre = arr.copy()
    pre[..., :3] *= arr[..., 3:4] / 255
    small = np.stack([np.asarray(Image.fromarray(pre[..., c].astype(np.float32), "F").resize((nw, nh), Image.LANCZOS)) for c in range(4)], axis=-1)
    a = np.clip(small[..., 3], 0, 255)
    rgb = np.clip(small[..., :3] / np.maximum(a[..., None] / 255, 1e-3), 0, 255)
    return rgb, a / 255


def vivid(rgb, alpha):
    """Lift dark art: gamma on brightness and a saturation boost, so it reads at retro size on a dark background."""
    x = np.clip(rgb / 255.0, 0, 1)
    mean = x.mean(axis=2, keepdims=True)
    x = np.clip(mean + (x - mean) * 1.35, 0, 1)
    x = np.power(x, 0.72)
    return x * 255


def pixelate(rgb, alpha, colors, hard, outline):
    # Alpha: hard edge for ships and pickups, four steps for glows.
    if hard:
        alpha = (alpha > 0.45).astype(np.float32)
    else:
        alpha = np.round(np.clip(alpha, 0, 1) * 4) / 4
        alpha[alpha < 0.25] = 0
    opaque = alpha > 0
    out = np.zeros(rgb.shape[:2] + (4,), np.uint8)
    if opaque.sum():
        pixels = Image.fromarray(rgb[opaque][None].astype(np.uint8), "RGB")
        quant = pixels.quantize(colors=min(colors, max(2, int(opaque.sum()))), method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE).convert("RGB")
        out[opaque, :3] = np.asarray(quant)[0]
        out[..., 3] = (alpha * 255).astype(np.uint8)
    if outline:
        # A one-pixel dark outline keeps small ships readable on the starfield.
        padded = np.pad(out, ((1, 1), (1, 1), (0, 0)))
        solid = padded[..., 3] > 0
        ring = np.zeros_like(solid)
        for dy, dx in ((-1, 0), (1, 0), (0, -1), (0, 1)):
            ring |= np.roll(np.roll(solid, dy, 0), dx, 1)
        ring &= ~solid
        padded[ring] = (*RIM[outline], 255)
        out = padded
    return out


def roster_dart(path):
    """The drafts lay the Dart out at the same place relative to the sheet size: top-left frame."""
    img = Image.open(path)
    w, h = img.size
    return img.crop((int(w * 0.023), int(h * 0.105), int(w * 0.145), int(h * 0.316)))


def build(raw, out_dir):
    sprites = {}
    for name, (sheet, box, mode, rot, size, colors, outline, inset) in S.items():   # outline: the rim team or None
        path = os.path.join(raw, SHEET[sheet])
        if mode == "auto-roster":
            crop, mode = roster_dart(path), "flat"
        else:
            x0, y0, x1, y1 = box
            crop = Image.open(path).crop((x0 + inset, y0 + inset, x1 - inset, y1 - inset))
        color, alpha = cut(crop, mode)
        color, alpha = trim(color, alpha)
        img = to_image(color, alpha)
        if rot:
            img = img.rotate(rot, expand=True, resample=Image.BICUBIC)   # counter-clockwise degrees
        rgb, a = fit(img, (size[0] - (2 if outline else 0), size[1] - (2 if outline else 0)))
        if mode == "flat":
            rgb = vivid(rgb, a)
        sprites[name] = pixelate(rgb, a, colors, mode == "flat", outline)
    # Shelf packing, tallest first.
    order = sorted(sprites, key=lambda n: -sprites[n].shape[0])
    width, x, y, shelf, frames = 512, 0, 0, 0, {}
    for name in order:
        h, w = sprites[name].shape[:2]
        if x + w > width:
            x, y, shelf = 0, y + shelf + 1, 0
        frames[name] = [x, y, w, h]
        x, shelf = x + w + 1, max(shelf, h)
    atlas = np.zeros((y + shelf + 1, width, 4), np.uint8)
    for name, (fx, fy, w, h) in frames.items():
        atlas[fy:fy + h, fx:fx + w] = sprites[name]
    os.makedirs(out_dir, exist_ok=True)
    Image.fromarray(atlas, "RGBA").save(os.path.join(out_dir, "atlas.png"), optimize=True)
    with open(os.path.join(out_dir, "atlas.json"), "w") as f:
        json.dump({"version": 1, "frames": dict(sorted(frames.items()))}, f, separators=(",", ":"))
    preview = Image.new("RGBA", (atlas.shape[1] * 3, atlas.shape[0] * 3), (16, 18, 48, 255))
    preview.alpha_composite(Image.fromarray(atlas, "RGBA").resize((atlas.shape[1] * 3, atlas.shape[0] * 3), Image.NEAREST))
    preview.save(os.path.join(out_dir, "preview.png"))
    print(f"{len(frames)} sprites, atlas {atlas.shape[1]}x{atlas.shape[0]}")


if __name__ == "__main__":
    build(sys.argv[1], sys.argv[2])
