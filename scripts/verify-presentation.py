"""Independently check the decks downloaded by the presentation browser smoke.

Usage: python scripts/verify-presentation.py <browser-output-directory>
Requires python-pptx. The expected slide content comes from the browser inputs.
"""

import argparse
from hashlib import sha256
import json
from pathlib import Path
import re
import subprocess
from tempfile import TemporaryDirectory
from xml.etree import ElementTree
from zipfile import ZipFile

from pptx import Presentation
from pptx.enum.shapes import MSO_SHAPE_TYPE


def check(condition, message):
    if not condition:
        raise ValueError(message)


def lines(text):
    return [line.strip() for line in text.splitlines() if line.strip()]


def check_package(path):
    with ZipFile(path) as package:
        check(package.testzip() is None, "Damaged PPTX package")
        for name in package.namelist():
            if name.endswith(".rels"):
                relations = ElementTree.fromstring(package.read(name))
                check(
                    all(rel.get("TargetMode") != "External" for rel in relations),
                    "Export contains an external dependency",
                )


def verify_download(path, expected, digest):
    check_package(path)
    deck = Presentation(path)
    check(len(deck.slides) == len(expected), "Wrong slide count")
    check(abs(deck.slide_width / deck.slide_height - 16 / 9) < 0.001, "Expected widescreen")
    images = 0
    for index, (slide, source) in enumerate(zip(deck.slides, expected), 1):
        text = [shape.text for shape in slide.shapes if shape.has_text_frame]
        check(source["title"] in text, f"Slide {index} lacks a native title")
        content = [line for box in text for line in lines(box)]
        for line in lines(source["body"]):
            check(any(line in actual for actual in content), f"Slide {index} lost a body line")
        note = slide.notes_slide.notes_text_frame if slide.has_notes_slide else None
        actual_notes = lines(note.text) if note is not None else []
        check(actual_notes == lines(source["notes"]), f"Slide {index} notes differ")
        pictures = [s for s in slide.shapes if s.shape_type == MSO_SHAPE_TYPE.PICTURE]
        check(len(pictures) == int(source["image"]), f"Slide {index} image count differs")
        for picture in pictures:
            check(sha256(picture.image.blob).hexdigest() == digest, f"Slide {index} image differs")
            check(picture.width > 0 and picture.height > 0, f"Slide {index} image has zero area")
            images += 1
    return {"slides": len(deck.slides), "embedded_pictures": images}


def verify_printed_pages(pdf, slides):
    from pypdf import PdfReader

    pages = PdfReader(pdf).pages
    check(len(pages) == len(slides), f"{pdf.name} page count differs")
    for index, (page, slide) in enumerate(zip(pages, slides), 1):
        text = "".join((page.extract_text() or "").split())
        for field in ("title", "body"):
            check("".join(slide[field].split()) in text, f"{pdf.name} page {index} lost {field}")
        if slide["notes"]:
            check("".join(slide["notes"].split()) not in text, f"{pdf.name} printed speaker notes")
        if slide["image"]:
            check(len(page.images) > 0, f"{pdf.name} page {index} lost its image")
        ratio = float(page.mediabox.width) / float(page.mediabox.height)
        check(abs(ratio - 16 / 9) < 0.001, f"{pdf.name} page {index} is not widescreen")


def verify_with_impress(directory, executable, revisions, browser_pdf=None):
    # An independent, empty profile ensures existing desktop sessions stay separate.
    with TemporaryDirectory(prefix="impress-", dir=directory) as scratch:
        root = Path(scratch).resolve()
        subprocess.run(
            [str(executable), "-env:UserInstallation=" + (root / "profile").as_uri(),
             "--headless", "--norestore", "--convert-to", "pdf:impress_pdf_Export", "--outdir", str(root),
             *(str(directory / name) for name in revisions)],
            check=True, timeout=240, capture_output=True,
        )
        for name, slides in revisions.items():
            verify_printed_pages(root / Path(name).with_suffix(".pdf"), slides)
    if browser_pdf is not None:
        pdf, original = browser_pdf
        verify_printed_pages(directory / pdf, revisions[original])


def verify_dense_comic(directory, soffice):
    expected = json.loads((directory / "comic-dense-expected.json").read_text(encoding="utf-8-sig"))
    name = "comic-dense.pptx"
    check_package(directory / name)
    deck = Presentation(directory / name)
    check(len(deck.slides) > 1, "Dense storyboard did not paginate")
    rendered = []
    pieces = []
    for i, slide in enumerate(deck.slides):
        native = {shape.name: shape.text.replace("\v", "\n")
                  for shape in slide.shapes if shape.has_text_frame}
        title = expected["title"] if i == 0 else f"第 1 镜 · 续页 {i+1}"
        check(native.get("页标题") == title, "Continuation lost its panel attribution")
        body = native.get("页正文", "")
        check(bool(body) or i == 0, "Empty continuation")
        pictures = [s for s in slide.shapes if s.shape_type == MSO_SHAPE_TYPE.PICTURE]
        check(len(pictures) == int(i == 0), "Panel art repeated or missing")
        if pictures:
            check(sha256(pictures[0].image.blob).hexdigest() == expected["asset_sha256"],
                  "Dense panel art differs from the approved input")
        pieces.append(body)
        rendered.append(dict(title=title, body=body, image=bool(pictures), notes=""))
    check("".join(pieces) == expected["body"], "Repaginated native text differs from the input")
    if soffice:
        verify_with_impress(directory, soffice.resolve(), {name: rendered})
    return {"slides": len(rendered), "native_body_characters": len(expected["body"]),
            "embedded_pictures": 1, "independent_pdf": bool(soffice)}


def verify(directory, soffice=None, kind="presentation", dense=False):
    directory = directory.resolve()
    manifest = json.loads((directory / f"{kind}-expected.json").read_text(encoding="utf-8-sig"))
    digest = manifest["asset_sha256"]
    check(re.fullmatch("[0-9a-f]{64}", digest), "Missing original fixture image SHA-256")
    versions = {version["file"]: version["slides"] for version in manifest["versions"]}
    v1, v2, reopened = (f"{kind}-{suffix}.pptx" for suffix in ("v1", "v2", "v1-reopened"))
    check(set(versions) == {v1, v2, reopened}, "Missing a downloaded revision")
    original = versions[v1]
    check(original == versions[reopened], "Expected inputs for v1 differ")
    check(original != versions[v2], "Expected a real change between revisions")
    check(len(original) > 1 and any(s["image"] for s in original), "Expected multiple slides and a picture")
    result = {name: verify_download(directory / name, expected, digest)
              for name, expected in versions.items()}

    if kind == "comic":
        check(sum(bool(s["image"]) for s in original) >= 2, "Expected art reused on multiple panels")
        check([s["body"] for s in original] != [s["body"] for s in versions[v2]],
              "Expected a visible change to the panel text")
    deck = Presentation(directory / v1)
    first_title = next(s for s in deck.slides[0].shapes
                       if s.has_text_frame and s.text == original[0]["title"])
    edited_title = original[0]["title"] + "（编辑校验）"
    first_title.text = edited_title
    edited = directory / f"{kind}-edited-check.pptx"
    deck.save(edited)
    edited_expected = [{**original[0], "title": edited_title}, *original[1:]]
    result[edited.name] = verify_download(edited, edited_expected, digest)
    if soffice:
        browser_pdf = ("presentation-v1.pdf", v1) if kind == "presentation" else None
        verify_with_impress(directory, soffice.resolve(), {**versions, edited.name: edited_expected}, browser_pdf)
        result["verified_pdf_count"] = len(versions) + 1 + int(browser_pdf is not None)
    if dense:
        check(kind == "comic", "Dense verification expects storyboard exports")
        result["comic-dense.pptx"] = verify_dense_comic(directory, soffice)
        if soffice:
            result["verified_pdf_count"] += 1
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--kind", choices=("presentation","comic"), default="presentation")
    parser.add_argument("--dense", action="store_true", help="Check native continuation pages from the long-panel fixture")
    parser.add_argument("--soffice", type=Path, help="Also use LibreOffice and pypdf to verify printed pages")
    args = parser.parse_args()
    print(json.dumps(verify(args.directory, args.soffice, args.kind, args.dense), ensure_ascii=False, indent=2))
