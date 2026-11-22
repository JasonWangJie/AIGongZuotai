import base64
import gzip
import hashlib
import importlib.util
import io
import tarfile
import tempfile
import unittest
from pathlib import Path


spec = importlib.util.spec_from_file_location("verify_sbom", Path(__file__).with_name("verify-workbench-sbom.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class ReleaseInputsTest(unittest.TestCase):
    def test_matching_and_changed_gzip(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "main.js").write_bytes(b"one")
            with self.assertRaisesRegex(ValueError, "sets differ"):
                verifier.web_bytes(root)
            (root / "main.js.gz").write_bytes(gzip.compress(b"one", mtime=0))
            self.assertEqual(verifier.web_bytes(root)[1:], (2, 1))
            (root / "main.js.gz").write_bytes(gzip.compress(b"two", mtime=0))
            with self.assertRaisesRegex(ValueError, "different JavaScript"):
                verifier.web_bytes(root)

    def test_modified_install_and_archive(self):
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode="w:gz") as packed:
            item = tarfile.TarInfo("package/package.json")
            item.size = 2
            packed.addfile(item, io.BytesIO(b"{}"))
        data = stream.getvalue()
        digest = hashlib.sha512(data)
        h = digest.hexdigest()
        sri = "sha512-" + base64.b64encode(digest.digest()).decode()
        with tempfile.TemporaryDirectory() as temp:
            cache = Path(temp) / "cache"
            archive = cache / "sha512" / h[:2] / h[2:4] / h[4:]
            archive.parent.mkdir(parents=True)
            archive.write_bytes(data)
            install = Path(temp) / "package"
            install.mkdir()
            (install / "package.json").write_bytes(b"{}")
            self.assertEqual(verifier.installed_archive(cache, sri, install), 1)
            (install / "package.json").write_bytes(b"[]")
            with self.assertRaisesRegex(ValueError, "installed npm bytes differ"):
                verifier.installed_archive(cache, sri, install)
            archive.write_bytes(data + b"x")
            with self.assertRaisesRegex(ValueError, "archive integrity mismatch"):
                verifier.installed_archive(cache, sri, install)


if __name__ == "__main__":
    unittest.main()
