#!/usr/bin/env python3
"""Convert asciinema .cast to MP4 via pyte + Pillow + ffmpeg.

Usage: python3 scripts/cast2mp4.py docs/demo.cast docs/demo.mp4
"""
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

import pyte
from PIL import Image, ImageDraw, ImageFont

# Dracula-inspired color scheme
COLORS = {
    "bg": (30, 31, 41),
    "fg": (209, 213, 224),
    "black": (39, 41, 54),
    "red": (240, 82, 82),
    "green": (100, 220, 143),
    "yellow": (247, 191, 89),
    "blue": (97, 166, 245),
    "magenta": (199, 120, 220),
    "cyan": (87, 209, 219),
    "white": (209, 213, 224),
    "brightblack": (89, 95, 112),
    "brightred": (240, 82, 82),
    "brightgreen": (100, 220, 143),
    "brightyellow": (247, 191, 89),
    "brightblue": (97, 166, 245),
    "brightmagenta": (199, 120, 220),
    "brightcyan": (87, 209, 219),
    "brightwhite": (242, 244, 250),
}

ANSI_MAP = {
    "black": COLORS["black"],
    "red": COLORS["red"],
    "green": COLORS["green"],
    "brown": COLORS["yellow"],
    "blue": COLORS["blue"],
    "magenta": COLORS["magenta"],
    "cyan": COLORS["cyan"],
    "white": COLORS["white"],
    "default": COLORS["fg"],
}

CELL_W = 10
CELL_H = 20
PAD = 20
TITLE_BAR_H = 40
FPS = 10


def find_font():
    candidates = [
        "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
        "/usr/share/fonts/TTF/DejaVuSansMono.ttf",
        "/usr/share/fonts/truetype/ubuntu/UbuntuMono-R.ttf",
    ]
    for p in candidates:
        if os.path.exists(p):
            return ImageFont.truetype(p, 16)
    return ImageFont.load_default()


def render_frame(screen, cols, rows, font):
    w = cols * CELL_W + PAD * 2
    h = rows * CELL_H + PAD * 2 + TITLE_BAR_H
    img = Image.new("RGB", (w, h), COLORS["bg"])
    draw = ImageDraw.Draw(img)

    # Title bar
    draw.rectangle([0, 0, w, TITLE_BAR_H], fill=(45, 47, 60))
    # Window buttons
    for i, color in enumerate([(240, 82, 82), (247, 191, 89), (100, 220, 143)]):
        draw.ellipse([PAD + i * 25, 12, PAD + i * 25 + 16, 28], fill=color)
    # Title text
    draw.text((w // 2 - 80, 10), "k8s4claw — OpenClaw Demo", fill=COLORS["fg"], font=font)

    # Terminal content
    for row in range(rows):
        for col in range(cols):
            char = screen.buffer[row][col]
            if char.data == " " and char.fg == "default" and char.bg == "default":
                continue

            x = PAD + col * CELL_W
            y = TITLE_BAR_H + PAD + row * CELL_H

            # Background
            if char.bg != "default":
                bg = ANSI_MAP.get(char.bg, COLORS["bg"])
                draw.rectangle([x, y, x + CELL_W, y + CELL_H], fill=bg)

            # Foreground
            if char.data.strip():
                fg = ANSI_MAP.get(char.fg, COLORS["fg"])
                if char.bold:
                    fg = COLORS.get("bright" + char.fg, fg)
                draw.text((x, y), char.data, fill=fg, font=font)

    return img


def main():
    if len(sys.argv) < 3:
        print(f"Usage: {sys.argv[0]} input.cast output.mp4")
        sys.exit(1)

    cast_path = sys.argv[1]
    mp4_path = sys.argv[2]

    with open(cast_path) as f:
        header = json.loads(f.readline())
        events = [json.loads(line) for line in f if line.strip()]

    cols = header.get("width", 100)
    rows = header.get("height", 35)

    screen = pyte.Screen(cols, rows)
    stream = pyte.Stream(screen)
    font = find_font()

    with tempfile.TemporaryDirectory() as tmpdir:
        frame_idx = 0
        last_time = 0.0
        frame_interval = 1.0 / FPS

        for event in events:
            ts, etype, data = event[0], event[1], event[2]

            if etype == "o":
                stream.feed(data)

            # Generate frames for elapsed time
            while last_time + frame_interval <= ts:
                last_time += frame_interval
                img = render_frame(screen, cols, rows, font)
                img.save(os.path.join(tmpdir, f"frame_{frame_idx:06d}.png"))
                frame_idx += 1

        # Hold last frame for 2 seconds
        for _ in range(FPS * 2):
            img = render_frame(screen, cols, rows, font)
            img.save(os.path.join(tmpdir, f"frame_{frame_idx:06d}.png"))
            frame_idx += 1

        print(f"Rendered {frame_idx} frames")

        # ffmpeg: frames -> mp4
        subprocess.run([
            "ffmpeg", "-y",
            "-framerate", str(FPS),
            "-i", os.path.join(tmpdir, "frame_%06d.png"),
            "-c:v", "libx264",
            "-pix_fmt", "yuv420p",
            "-preset", "slow",
            "-crf", "18",
            mp4_path,
        ], check=True, capture_output=True)

        print(f"MP4 saved to {mp4_path}")


if __name__ == "__main__":
    main()
