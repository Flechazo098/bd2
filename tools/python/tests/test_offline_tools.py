from __future__ import annotations

import base64
import json
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile

from Crypto.Cipher import AES


TOOLS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS))

import gamedata_db
import import_seed
import save_checkpoint


class Arguments:
    pass


class GameDataToolTests(unittest.TestCase):
    def test_extracts_and_decrypts_logical_database(self):
        plain = bytearray(gamedata_db.PAGE_SIZE)
        plain[: len(gamedata_db.HEADER)] = gamedata_db.HEADER
        key = gamedata_db.derive_key()
        encrypted = AES.new(key, AES.MODE_CBC, gamedata_db.HEADER).encrypt(bytes(plain))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = root / "123" / "release"
            release.mkdir(parents=True)
            with zipfile.ZipFile(release / gamedata_db.ARCHIVE_NAME, "w") as archive:
                archive.writestr(gamedata_db.database_entry("pack21"), encrypted)
            self.assertEqual(gamedata_db.read_database(root, "123", "pack21"), bytes(plain))

    def test_wire_field_selection(self):
        proto = import_seed.encode_field(1, 0, 42) + import_seed.encode_field(3, 2, b"abc")
        decoded = gamedata_db.wire_fields(proto, {1, 3})
        self.assertEqual(decoded[1][0]["varint"], 42)
        self.assertEqual(decoded[3][0]["utf8"], "abc")


class ImportToolTests(unittest.TestCase):
    def test_login_import_removes_captured_key(self):
        user = import_seed.encode_field(1, 0, 7)
        user += import_seed.encode_field(3, 2, b"captured-secret")
        proto = import_seed.encode_field(1, 2, user) + import_seed.encode_field(4, 0, 9)
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / "login.pb"
            source.write_bytes(proto)
            args = Arguments()
            args.input, args.packet_code = source, 11
            result = import_seed.import_login(args)
        imported_user = base64.b64decode(result["user_info_base64"])
        self.assertFalse(any(field.number == 3 for field in import_seed.fields(imported_user)))
        self.assertEqual(base64.b64decode(result["response_fields_base64"]), import_seed.encode_field(4, 0, 9))

    def test_starter_and_mail_import(self):
        item = b"".join(
            import_seed.encode_field(number, 0, value)
            for number, value in ((1, 10), (2, 8), (3, 8), (4, 3))
        )
        costume = import_seed.encode_field(1, 0, 20) + import_seed.encode_field(2, 0, 3501)
        character = import_seed.encode_field(1, 0, 30) + import_seed.encode_field(2, 0, 350)
        mail = b"".join(
            import_seed.encode_field(number, 0, value)
            for number, value in ((1, 1), (2, 2), (7, 100), (13, 50))
        )
        mail += import_seed.encode_field(8, 2, import_seed.encode_varint(8))
        mail += import_seed.encode_field(9, 2, import_seed.encode_varint(7))
        mail += import_seed.encode_field(10, 2, import_seed.encode_varint(3))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name, proto in {
                "items.pb": import_seed.encode_field(1, 2, item),
                "costumes.pb": import_seed.encode_field(1, 2, costume),
                "characters.pb": import_seed.encode_field(1, 2, character),
                "mail.pb": import_seed.encode_field(1, 2, mail)
                + import_seed.encode_field(2, 0, 2)
                + import_seed.encode_field(3, 0, 1),
            }.items():
                (root / name).write_bytes(proto)
            args = Arguments()
            args.items, args.costumes, args.characters = (
                root / "items.pb",
                root / "costumes.pb",
                root / "characters.pb",
            )
            starter = import_seed.import_starter(args)
            args.input = root / "mail.pb"
            mailbox = import_seed.import_mail(args)
        self.assertEqual(starter["items"][0]["count"], 3)
        self.assertEqual(starter["costumes"][0]["id"], 3501)
        self.assertEqual(starter["characters"][0]["id"], 350)
        self.assertEqual(mailbox["mails"][0]["reward_counts"], [3])


class CheckpointToolTests(unittest.TestCase):
    def test_checkpoint_hash_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = root / "state"
            target = root / "checkpoint"
            state.mkdir()
            for name in save_checkpoint.STATE_FILES:
                (state / name).write_text(json.dumps({"name": name}), encoding="utf-8")
            created = save_checkpoint.create(state, target, "test")
            self.assertEqual(save_checkpoint.verify(target), created)


if __name__ == "__main__":
    unittest.main()
