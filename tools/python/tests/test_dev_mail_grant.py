"""Currency grant CLI regressions; runnable without third-party packages."""

from __future__ import annotations

from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

TOOLS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS))
import dev_mail_grant


class CurrencyMailGrantTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name) / "nested" / "grants.json"

    def args(self, *extra):
        return dev_mail_grant.parser().parse_args([
            "grant", "--output", str(self.output), "--attachment", "4:0:123", *extra,
        ])

    def run_grant(self, *extra):
        stream = io.StringIO()
        with redirect_stdout(stream):
            self.assertEqual(dev_mail_grant.grant(self.args(*extra)), 0)
        return json.loads(stream.getvalue())

    def child(self, *extra):
        # -S removes site packages, proving the command can run without Crypto.
        return [sys.executable, "-S", str(TOOLS / "dev_mail_grant.py"),
                "grant", "--output", str(self.output), "--attachment", "4:0:123", *extra]

    def test_multiple_attachments_create_one_mail_and_preserve_duplicates(self):
        result = self.run_grant("--identity", "release-1", "--title", " 发放测试 ",
                                "--body", " 正文 ", "--attachment", "3:0:5",
                                "--attachment", "4:0:2", "--attachment", "12:0:1",
                                "--attachment", "20:0:2147483647")
        value = dev_mail_grant.load_grants(self.output)
        self.assertEqual(value["version"], 1)
        self.assertEqual(len(value["grants"]), 1)
        self.assertEqual(value["grants"][0], result["grant"])
        self.assertEqual(result["grant"]["title"], "发放测试")
        self.assertEqual(result["grant"]["body"], "正文")
        self.assertGreater(result["grant"]["sent_at"], 0)
        self.assertEqual(result["grant"]["rewards"], [
            {"type": 4, "id": 0, "count": 123}, {"type": 3, "id": 0, "count": 5},
            {"type": 4, "id": 0, "count": 2}, {"type": 12, "id": 0, "count": 1},
            {"type": 20, "id": 0, "count": 2147483647},
        ])

    def test_identity_retry_keeps_original_timestamp_and_does_not_write(self):
        first = self.run_grant("--identity", "once")
        original = self.output.read_bytes()
        with mock.patch.object(dev_mail_grant, "atomic_json", side_effect=AssertionError("must not write")):
            second = self.run_grant("--identity", "once")
        self.assertTrue(first["created"])
        self.assertFalse(second["created"])
        self.assertEqual(first["grant"], second["grant"])
        self.assertEqual(self.output.read_bytes(), original)

    def test_identity_conflict_is_rejected_without_changing_file(self):
        self.run_grant("--identity", "once")
        original = self.output.read_bytes()
        for extra in (("--title", "another"), ("--body", "another"), ("--attachment", "3:0:1")):
            with self.subTest(extra=extra), self.assertRaisesRegex(ValueError, "内容不同"):
                self.run_grant("--identity", "once", *extra)
        self.assertEqual(self.output.read_bytes(), original)

    def test_omitted_identity_appends_distinct_grants(self):
        first, second = self.run_grant(), self.run_grant()
        self.assertNotEqual(first["grant"]["identity"], second["grant"]["identity"])
        self.assertEqual(len(dev_mail_grant.load_grants(self.output)["grants"]), 2)

    def test_invalid_attachments_are_rejected(self):
        for attachment in ("8:1:1", "4:1:1", "4:0:0", "4:0:-1", "4:0:2147483648", "4:0", "4:0:1:2", "4:0:true"):
            with self.subTest(attachment=attachment), self.assertRaises(SystemExit), redirect_stdout(io.StringIO()):
                with mock.patch("sys.stderr", new=io.StringIO()):
                    self.args("--attachment", attachment)
        self.assertFalse(self.output.exists())

    def test_invalid_title_body_or_identity_does_not_create_output(self):
        for extra in (("--title", " "), ("--title", "x" * 501), ("--body", "x" * 5001),
                      ("--identity", " "), ("--identity", "x" * 501)):
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                self.run_grant(*extra)
        self.assertFalse(self.output.exists())

    def test_existing_invalid_schema_is_preserved(self):
        entry = {"identity": "one", "title": "title", "body": "body", "sent_at": 1,
                 "rewards": [{"type": 4, "id": 0, "count": 1}]}
        invalid_values = [
            [], {"version": True, "grants": []}, {"version": 1, "grants": [], "extra": 1},
            {"version": "2.35.10", "mails": []}, {"version": 1, "grants": [entry, entry]},
            {"version": 1, "grants": [{**entry, "sent_at": True}]},
            {"version": 1, "grants": [{**entry, "extra": 1}]},
            {"version": 1, "grants": [{**entry, "rewards": []}]},
            {"version": 1, "grants": [{**entry, "rewards": [{"type": 4, "id": False, "count": 1}]}]},
            {"version": 1, "grants": [{**entry, "rewards": [{"type": 4, "id": 0, "count": True}]}]},
        ]
        self.output.parent.mkdir(parents=True)
        for value in invalid_values:
            original = json.dumps(value).encode()
            self.output.write_bytes(original)
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.run_grant()
            self.assertEqual(self.output.read_bytes(), original)
        self.output.write_text("broken JSON", encoding="utf-8")
        with self.assertRaises(ValueError):
            self.run_grant()
        self.assertEqual(self.output.read_text(encoding="utf-8"), "broken JSON")

    def test_failed_atomic_replace_preserves_previous_file_and_cleans_temporary(self):
        self.run_grant("--identity", "one")
        original = self.output.read_bytes()
        with mock.patch.object(dev_mail_grant.os, "replace", side_effect=OSError("injected failure")):
            with self.assertRaises(OSError):
                self.run_grant("--identity", "two")
        self.assertEqual(self.output.read_bytes(), original)
        self.assertEqual(list(self.output.parent.glob("*.tmp")), [])

    def test_cli_uses_standard_library_and_reports_validation_errors(self):
        result = subprocess.run(self.child("--identity", "standalone"), capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)["created"])
        result = subprocess.run(self.child("--identity", "standalone", "--title", "conflict"), capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 1)
        self.assertIn("dev_tools:", result.stderr)

    def test_process_waits_for_lock_then_reads_latest_file(self):
        with dev_mail_grant.grant_file_lock(self.output):
            process = subprocess.Popen(self.child("--identity", "child"), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                with self.assertRaises(subprocess.TimeoutExpired):
                    process.wait(timeout=0.5)
                entry = {"identity": "parent", "title": "title", "body": "body", "sent_at": 1,
                         "rewards": [{"type": 4, "id": 0, "count": 1}]}
                dev_mail_grant.atomic_json(self.output, {"version": 1, "grants": [entry]})
            except BaseException:
                process.kill()
                process.communicate()
                raise
        stdout, stderr = process.communicate(timeout=20)
        self.assertEqual(process.returncode, 0, stderr)
        self.assertTrue(json.loads(stdout)["created"])
        self.assertEqual([item["identity"] for item in dev_mail_grant.load_grants(self.output)["grants"]], ["parent", "child"])

    def test_parallel_processes_preserve_all_grants_and_shared_identity_once(self):
        identities = [f"child-{i}" for i in range(8)] + ["shared"] * 4
        processes = [subprocess.Popen(self.child("--identity", identity), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                     for identity in identities]
        try:
            results = []
            for process in processes:
                stdout, stderr = process.communicate(timeout=30)
                self.assertEqual(process.returncode, 0, stderr)
                results.append(json.loads(stdout))
            grants = dev_mail_grant.load_grants(self.output)["grants"]
            self.assertEqual({entry["identity"] for entry in grants}, set(identities))
            self.assertEqual(len(grants), 9)
            self.assertEqual(sum(result["created"] for result in results), 9)
        finally:
            for process in processes:
                if process.poll() is None:
                    process.kill()
                process.communicate()


if __name__ == "__main__":
    unittest.main()
