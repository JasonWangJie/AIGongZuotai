"""Verify a local release-sbom.mjs output against the actual offline release inputs."""

import argparse
import base64
import gzip
import hashlib
import io
import json
import os
import subprocess
import tarfile
from pathlib import Path, PurePosixPath

from jsonschema import Draft7Validator


SCHEMA_SHA256 = "239208b7ac287b3cf5d9a9af23f9d69863971102a5e1587a27a398b43490b89b"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read_json(path):
    return json.loads(path.read_bytes())


def file_map(root):
    return {p.relative_to(root).as_posix(): p for p in root.rglob("*") if p.is_file()}


def tree_hash(files):
    tree = hashlib.sha256()
    for name, path in sorted(files.items()):
        tree.update(name.encode() + b"\0" + digest(path.read_bytes()).encode() + b"\0")
    return tree.hexdigest()


def web_bytes(dist):
    files = file_map(dist)
    scripts = {name for name in files if name.endswith(".js")}
    require(scripts and {n[:-3] for n in files if n.endswith(".gz")} == scripts,
            "gzip and JS sets differ")
    for name in scripts:
        require(gzip.decompress(files[name + ".gz"].read_bytes()) == files[name].read_bytes(),
                "gzip expands to different JavaScript")
    return tree_hash(files), len(files), len(scripts)


def installed_archive(cache, archive_sri, directory):
    algorithm, checksum = archive_sri.split("-", 1)
    require(algorithm in {"sha256", "sha384", "sha512"}, "unsupported archive checksum")
    h = base64.b64decode(checksum, validate=True).hex()
    archive = (cache / algorithm / h[:2] / h[2:4] / h[4:]).read_bytes()
    require(hashlib.new(algorithm, archive).hexdigest() == h, "npm archive integrity mismatch")
    entries = set()
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as packed:
        for item in packed:
            if item.isdir():
                continue
            path = PurePosixPath(item.name)
            require(item.isfile() and not path.is_absolute() and len(path.parts) >= 2
                    and ".." not in path.parts, "unsupported npm archive entry")
            name = "/".join(path.parts[1:])
            require(name not in entries, "duplicate npm archive file")
            entries.add(name)
            require((directory / name).read_bytes() == packed.extractfile(item).read(),
                    "installed npm bytes differ from the locked archive")
    installed = {n for n in file_map(directory) if "node_modules" not in PurePosixPath(n).parts}
    require(entries == installed, "installed npm file set differs")
    return len(entries)


def verify(args):
    root = args.root.resolve()
    web = root / "apps/workbench"
    require(digest(args.schema.read_bytes()) == SCHEMA_SHA256, "SPDX schema checksum mismatch")
    doc = read_json(args.sbom)
    Draft7Validator(read_json(args.schema)).validate(doc)
    packages = {p["SPDXID"]: p for p in doc["packages"]}
    require(len(packages) == len(doc["packages"]), "duplicate package ID")
    document = doc["SPDXID"]
    edges = {(r["spdxElementId"], r["relationshipType"], r["relatedSpdxElement"])
             for r in doc["relationships"]}
    require(len(edges) == len(doc["relationships"]), "duplicate relationship")
    for start, _, end in edges:
        require((start == document or start in packages) and end in packages, "dangling relationship")
    described = [packages[end] for start, kind, end in edges if start == document and kind == "DESCRIBES"]
    require(len(described) == 2, "expected two independently described release packages")
    lock_file = web / "package-lock.json"
    lock = read_json(lock_file)
    require(lock["lockfileVersion"] == 3, "unsupported npm lockfile")
    server = next(p for p in described if p["name"] == "aigongzuotai/cmd/server")
    frontend = next(p for p in described if p["name"] == lock["name"])
    binary = digest(args.server.read_bytes())
    require(binary == digest(args.rebuild.read_bytes()), "independent binary rebuild differs")
    dist, nfiles, ngzip = web_bytes(args.dist)
    for pkg, checksum in [(server, binary), (frontend, dist)]:
        require(pkg["checksums"] == [{"algorithm": "SHA256", "checksumValue": checksum}],
                "described package checksum differs")
    lock_sha = digest(lock_file.read_bytes())
    require(frontend["sourceInfo"].endswith(f"{nfiles} files. npm lockfile SHA256 {lock_sha}"),
            "npm lock or reproduced file count differs")
    env = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local")
    def go(*parts):
        return subprocess.check_output([str(args.go.resolve()), *parts], cwd=root, env=env)
    info = json.loads(go("version", "-m", "-json", str(args.server.resolve())))
    require(info["Path"] == server["name"] and info["Main"]["Version"] == server["versionInfo"],
            "server build identity differs")
    require(go("env", "GOVERSION").decode().strip() == info["GoVersion"], "Go version differs")
    require(go("mod", "verify").decode().strip() == "all modules verified", "Go module cache verification failed")
    own_files = {"go.mod": root / "go.mod", "go.sum": root / "go.sum"}
    fields = ("GoFiles", "CgoFiles", "SFiles", "CFiles", "CXXFiles", "HFiles", "SysoFiles", "EmbedFiles")
    template = "{{if and .Module .Module.Main}}" + "".join(
        "{{range ." + field + "}}{{$.Dir}}{{printf \"\\t\"}}{{.}}{{printf \"\\n\"}}{{end}}" for field in fields
    ) + "{{end}}"
    for line in go("list", "-deps", "-f", template, "./cmd/server").decode().splitlines():
        if line:
            directory, filename = line.split("\t")
            path = Path(directory, filename)
            own_files[path.relative_to(root).as_posix()] = path
    web_inputs = {"src/" + name: path for name, path in file_map(web / "src").items()}
    for filename in ("index.html", "vite.config.ts", "package.json", "package-lock.json", "tsconfig.json"):
        web_inputs[filename] = web / filename
    web_inputs.update({"public/" + n: p for n, p in file_map(web / "public").items()})
    sums = set((root / "go.sum").read_text().splitlines())
    expected_edges = {(document, "DESCRIBES", p["SPDXID"]) for p in described}
    def package(name, version, source=None):
        rows = [p for p in packages.values() if p["name"] == name and p["versionInfo"] == version
                and (source is None or p.get("sourceInfo", "").endswith(source))]
        require(len(rows) == 1, "missing or ambiguous dependency identity")
        return rows[0]["SPDXID"]
    expected_edges.add((server["SPDXID"], "DEPENDS_ON", package("Go standard library and runtime", info["GoVersion"])))
    for dep in info["Deps"]:
        require("Replace" not in dep and " ".join(dep[k] for k in ("Path", "Version", "Sum")) in sums,
                "Go dependency identity differs from go.sum")
        expected_edges.add((server["SPDXID"], "DEPENDS_ON", package(dep["Path"], dep["Version"], dep["Sum"])))
    production = {key: val for key, val in lock["packages"].items() if key and not val.get("dev")}
    identities = {"": frontend["SPDXID"]}
    installed_files = 0
    for key, val in production.items():
        path = PurePosixPath(key)
        require(not path.is_absolute() and ".." not in path.parts and path.parts[0] == "node_modules",
                "invalid locked npm location")
        installed_files += installed_archive(args.npm_cache, val["integrity"], web / key)
        manifest = read_json(web / key / "package.json")
        # The lockfile uses name overrides for npm aliases.
        name = val.get("name") or key.rsplit("node_modules/", 1)[-1]
        require(name == manifest["name"] and val["version"] == manifest["version"],
                "npm name or version differs")
        identities[key] = package(name, val["version"], val["integrity"])
    for key, val in {"": lock["packages"][""], **production}.items():
        for dep in set(val.get("dependencies", {})) | set(val.get("optionalDependencies", {})):
            parent = key
            while True:
                child = (parent + "/" if parent else "") + "node_modules/" + dep
                if child in identities:
                    expected_edges.add((identities[key], "DEPENDS_ON", identities[child]))
                    break
                if not parent:
                    require(dep in val.get("optionalDependencies", {}), "unresolved npm dependency")
                    break
                parent = parent.rsplit("/node_modules/", 1)[0] if "/node_modules/" in parent else ""
    require(edges == expected_edges, "dependency relationship differs from original sources")
    require(set(packages) == {end for _, _, end in expected_edges}, "unaccounted package")
    return dict(server_sha256=binary, dist_sha256=dist, dist_files=nfiles, verified_gzip_pairs=ngzip,
                go_version=info["GoVersion"], go_modules=len(info["Deps"]), go_sum_sha256=digest((root/"go.sum").read_bytes()),
                go_module_input_files=len(own_files), go_module_inputs_sha256=tree_hash(own_files),
                workbench_source_files=len(web_inputs), workbench_sources_sha256=tree_hash(web_inputs),
                npm_lock_sha256=lock_sha, npm_archives=len(production), npm_installed_files=installed_files,
                packages=len(packages), relationships=len(edges), schema_sha256=SCHEMA_SHA256,
                spdx_sha256=digest(args.sbom.read_bytes()))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for option in ("root", "server", "rebuild", "dist", "sbom", "schema", "npm-cache", "go"):
        parser.add_argument("--" + option, type=Path, required=True)
    print(json.dumps(verify(parser.parse_args()), indent=2))
