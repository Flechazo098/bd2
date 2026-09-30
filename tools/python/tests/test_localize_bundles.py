import json
import os
import pathlib
import sys
import tempfile
import unittest

TOOLS = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS))

import localize_bundles


class LocalizeBundlesTest(unittest.TestCase):
    def test_uses_source_catalog_and_hard_links_every_reference(self):
        with tempfile.TemporaryDirectory() as root:
            source = pathlib.Path(root, "release")
            aa = pathlib.Path(root, "aa")
            source.mkdir()
            aa.mkdir()
            bundle = source / "nested" / "current.bundle"
            bundle.parent.mkdir()
            bundle.write_bytes(b"current bundle")
            builtin = aa / "builtin.bundle"
            builtin.write_bytes(b"builtin")
            old_catalog = {"m_InternalIds": [localize_bundles.LOCAL_PREFIX + "obsolete.bundle"]}
            (aa / "catalog.json").write_text(json.dumps(old_catalog), encoding="utf-8")
            current_catalog = {
                "m_BuildResultHash": "current",
                "m_InternalIds": [
                    localize_bundles.REMOTE_PREFIX
                    + "StandaloneWindows64\\HD\\20260921135230\\nested/current.bundle",
                    localize_bundles.LOCAL_PREFIX + "builtin.bundle",
                ],
            }
            (source / "catalog_alpha.json").write_text(json.dumps(current_catalog), encoding="utf-8")

            result = localize_bundles.prepare_catalog(str(source), str(aa))

            localized = json.loads((aa / "catalog.json").read_text(encoding="utf-8"))
            self.assertEqual("current", localized["m_BuildResultHash"])
            self.assertEqual(
                localize_bundles.LOCAL_PREFIX + "nested\\current.bundle",
                localized["m_InternalIds"][0],
            )
            self.assertTrue(os.path.samefile(bundle, aa / "nested" / "current.bundle"))
            self.assertEqual(1, result["linked"])
            self.assertTrue((aa / localize_bundles.BACKUP_NAME).is_file())

            repeated = localize_bundles.prepare_catalog(str(source), str(aa))
            self.assertEqual(0, repeated["linked"])
            self.assertEqual(1, repeated["existing"])
            self.assertEqual(result["catalog_sha256"], repeated["catalog_sha256"])

    def test_missing_referenced_bundle_does_not_replace_catalog(self):
        with tempfile.TemporaryDirectory() as root:
            source = pathlib.Path(root, "release")
            aa = pathlib.Path(root, "aa")
            source.mkdir()
            aa.mkdir()
            original = b'{"m_InternalIds":["original"]}'
            (aa / "catalog.json").write_bytes(original)
            catalog = {
                "m_InternalIds": [
                    localize_bundles.REMOTE_PREFIX
                    + "StandaloneWindows64\\HD\\20260921135230\\missing.bundle"
                ]
            }
            (source / "catalog_alpha.json").write_text(json.dumps(catalog), encoding="utf-8")

            with self.assertRaises(FileNotFoundError):
                localize_bundles.prepare_catalog(str(source), str(aa))

            self.assertEqual(original, (aa / "catalog.json").read_bytes())
            self.assertFalse((aa / localize_bundles.BACKUP_NAME).exists())

    def test_conflicting_existing_bundle_does_not_replace_catalog(self):
        with tempfile.TemporaryDirectory() as root:
            source = pathlib.Path(root, "release")
            aa = pathlib.Path(root, "aa")
            source.mkdir()
            aa.mkdir()
            (source / "current.bundle").write_bytes(b"source")
            (aa / "current.bundle").write_bytes(b"conflict")
            original = b'{"m_InternalIds":["original"]}'
            (aa / "catalog.json").write_bytes(original)
            (source / "catalog_alpha.json").write_text(json.dumps({
                "m_InternalIds": [
                    localize_bundles.REMOTE_PREFIX
                    + "StandaloneWindows64\\HD\\20260921135230\\current.bundle"
                ]
            }), encoding="utf-8")

            with self.assertRaises(FileExistsError):
                localize_bundles.prepare_catalog(str(source), str(aa))

            self.assertEqual(original, (aa / "catalog.json").read_bytes())

    def test_traversal_reference_is_rejected(self):
        with self.assertRaises(ValueError):
            localize_bundles.relative_bundle(
                localize_bundles.REMOTE_PREFIX
                + "StandaloneWindows64\\HD\\20260921135230\\..\\escape.bundle"
            )


if __name__ == "__main__":
    unittest.main()
