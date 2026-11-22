"""Refresh embedded public-domain user-country ranges; run explicitly before rebuilding."""

import csv
import hashlib
import ipaddress
import json
from pathlib import Path
import urllib.request


ROOT = Path(__file__).resolve().parent
RELEASE = "https://api.github.com/repos/sapics/ip-location-db/releases/tags/latest"
CODES = {"CN": 1, "HK": 2, "MO": 2, "TW": 2, "JP": 3}


def load_asset(asset, family):
    with urllib.request.urlopen(asset["browser_download_url"], timeout=90) as response:
        data = response.read(64 << 20)
    digest = "sha256:" + hashlib.sha256(data).hexdigest()
    if len(data) != asset["size"] or digest != asset["digest"]:
        raise ValueError("Upstream country data differs from the release digest")
    records = bytearray()
    last = None
    seen = set()
    for first, end, country in csv.reader(data.decode("ascii").splitlines()):
        if country not in CODES:
            continue
        first, end = ipaddress.ip_address(first), ipaddress.ip_address(end)
        if first.version != family or end.version != family or first > end or (last and first <= last):
            raise ValueError("Unsorted or overlapping country ranges")
        records.extend(first.packed + end.packed + bytes([CODES[country]]))
        seen.add(country)
        last = end
    if seen != CODES.keys():
        raise ValueError("Incomplete country coverage")
    return records


def main():
    with urllib.request.urlopen(RELEASE, timeout=30) as response:
        assets = {asset["name"]: asset for asset in json.load(response)["assets"]}
    output = {}
    sources = [
        "Source: https://github.com/sapics/ip-location-db (user-country)",
        "License: Open Data Commons Public Domain Dedication and License 1.0",
        "https://opendatacommons.org/licenses/pddl/1-0/",
        "Only CN/HK/MO/TW/JP ranges are retained and mapped to display languages.",
        "",
    ]
    for family in (4, 6):
        asset = assets[f"user-country-ipv{family}.csv"]
        output[ROOT / f"ipv{family}.bin"] = load_asset(asset, family)
        sources += [asset["browser_download_url"], asset["updated_at"], asset["digest"], ""]
    output[ROOT / "SOURCES.txt"] = "\n".join(sources).encode("ascii")
    for path, data in output.items():
        path.write_bytes(data)


if __name__ == "__main__":
    main()
