"""Independently decode the two authenticated downloads from the video editor smoke."""

import argparse
from array import array
import hashlib
import json
import math
from pathlib import Path
import subprocess
import sys


def check(condition, message):
    if not condition:
        raise ValueError(message)


def run(tool, *args):
    return subprocess.run([tool, *args], stdin=subprocess.DEVNULL, capture_output=True,
                          check=True, timeout=45).stdout


def frame_rows(tool, path):
    # A white square moves three source pixels per frame on synthetic red/blue footage.
    # Sampling a row through its middle measures both shot identity and original time.
    raw = run(tool, "-nostdin", "-v", "error", "-i", str(path), "-an", "-vf",
              "scale=320:180:flags=area,format=rgb24,crop=320:1:0:24",
              "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
    row_bytes = 320 * 3
    check(raw and len(raw) % row_bytes == 0, "incomplete RGB row")
    rows = []
    for offset in range(0, len(raw), row_bytes):
        row = raw[offset:offset + row_bytes]
        red, green, blue = row[:3]
        check(max(red, blue) > 170 and green < 70 and abs(red-blue) > 140, "unknown shot colour")
        marker = [x for x in range(320) if min(row[3*x:3*x+3]) > 205]
        check(28 <= len(marker) <= 34 and marker[-1]-marker[0] < 35, "missing source motion marker")
        rows.append(("red" if red > blue else "blue", (marker[0]+marker[-1])/2))
    return rows


def audio_pitch(tool, path, midpoint):
    raw = run(tool, "-nostdin", "-v", "error", "-i", str(path), "-ss", f"{midpoint-0.1:.6f}",
              "-t", "0.2", "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
    samples = array("h", raw)
    if sys.byteorder != "little":
        samples.byteswap()
    check(len(samples) >= 3000 and max(samples) > 250, "missing audible samples")
    crossings = [i for i in range(1, len(samples)) if samples[i-1] < 0 <= samples[i]]
    check(len(crossings) >= 20, "missing tone")
    return round((len(crossings)-1)*16000/(crossings[-1]-crossings[0]), 1)


def inspect(args, version, sources):
    path = args.directory / f"timeline-v{version['version']}.mp4"
    check(path.name == version["file"], "unexpected export file")
    info = json.loads(run(args.ffprobe, "-v", "error", "-show_streams", "-show_format",
                          "-of", "json", str(path)))
    video, audio = info["streams"]
    check((video["codec_name"], video["width"], video["height"], video["avg_frame_rate"]) ==
          ("h264", 1280, 720, "24/1"), "unexpected picture format")
    check((audio["codec_name"], audio["sample_rate"], audio["channels"]) ==
          ("aac", "48000", 2), "unexpected audio format")
    output = frame_rows(args.ffmpeg, path)
    clips = []
    elapsed = start_frame = 0
    for clip in version["clips"]:
        source = clip["source"]
        length = clip["out_ms"] - clip["in_ms"]
        pitch = audio_pitch(args.ffmpeg, path, (elapsed + length/2)/1000)
        check(abs(pitch - {"red": 440, "blue": 880}[source]) <= 12, "incorrect audio segment")
        elapsed += length
        end_frame = (elapsed*24 + 500)//1000
        first = math.ceil(clip["in_ms"]*24/1000)
        part = output[start_frame:end_frame]
        check(len(part) == end_frame-start_frame and len(part) > 1, "missing cut frames")
        errors = []
        for i, (colour, marker) in enumerate(part):
            check(colour == source, "wrong source or cut order")
            errors.append(abs(marker - sources[source][first+i][1]))
        check(max(errors) <= 4, f"{path.name}: original frame offset is wrong ({max(errors)} px)")
        clips.append(dict(source=source, in_ms=clip["in_ms"], out_ms=clip["out_ms"],
                          frames=len(part), max_marker_error_px=max(errors), measured_tone_hz=pitch))
        start_frame = end_frame
    duration_ms = round(float(info["format"]["duration"])*1000)
    check(elapsed == version["duration_ms"] and len(output) == start_frame and
          abs(duration_ms-elapsed) <= 42, "incorrect final duration")
    check(abs(float(audio["duration"]) - len(output)/24) < 0.045, "audio/video end mismatch")
    return dict(version=version["version"], file=path.name, sha256=hashlib.sha256(path.read_bytes()).hexdigest(),
                frames=len(output), duration_ms=duration_ms, clips=clips)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--ffmpeg", required=True)
    parser.add_argument("--ffprobe", required=True)
    parser.add_argument("--panel-replacement", action="store_true")
    args = parser.parse_args()
    if args.panel_replacement:
        # Independent expectations from the test's two fixed source cuts.
        first = dict(source="red", in_ms=250, out_ms=1250)
        old = dict(source="red", in_ms=500, out_ms=1750)
        new = dict(source="blue", in_ms=500, out_ms=1750)
        versions = [
            dict(version=1, file="timeline-v1.mp4", duration_ms=2250, clips=[first, old]),
            dict(version=2, file="timeline-v2.mp4", duration_ms=2250, clips=[first, new]),
        ]
    else:
        expected = json.loads((args.directory / "video-edit-expected.json").read_text(encoding="utf-8"))
        versions = expected["versions"]
        check([v["version"] for v in versions] == [1, 2] and
              expected["restored_version"] == 3, "incomplete browser export set")
    sources = {s: frame_rows(args.ffmpeg, args.directory / f"source-{s}.mp4") for s in ("red", "blue")}
    check(all(len(rows) == 72 and all(colour == source for colour, _ in rows)
              for source, rows in sources.items()), "wrong original files")
    results = [inspect(args, version, sources) for version in versions]
    check(results[0]["sha256"] != results[1]["sha256"], "both revisions have identical bytes")
    print(json.dumps({"scope": "local authenticated Chromium downloads, synthetic source footage",
                      "video": "H.264 1280x720 24fps", "audio": "AAC 48kHz stereo",
                      "versions": results}, indent=2))


if __name__ == "__main__":
    main()
