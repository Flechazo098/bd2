from __future__ import annotations

import base64
import json
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile
from unittest import mock

from Crypto.Cipher import AES
from google.protobuf import descriptor_pb2


TOOLS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS))

import gamedata_db
import import_seed
import deobfuscate_client_source
import extract_client_proto
import dev_mail_grant
import inherited_stage


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


class DevelopmentMailGrantToolTests(unittest.TestCase):
    def test_deterministic_random_box_is_replaced_by_direct_material(self):
        items = [
            {"id": 400131, "element_type": 9, "name": "装备制作所需材料", "category": "随机箱"},
            {"id": 127, "element_type": 8, "name": "<未找到本地化文本 #32127>", "category": "资源"},
            {"id": 999, "element_type": 9, "name": "真正随机箱", "category": "随机箱"},
        ]
        mapped = dev_mail_grant.map_fixed_boxes_to_direct_items(
            items,
            {400131: (8, 127, 1)},
            {400131: {"女神之泪": 1}},
        )
        self.assertNotIn((9, 400131), {(item["element_type"], item["id"]) for item in mapped})
        self.assertNotIn((9, 999), {(item["element_type"], item["id"]) for item in mapped})
        material = next(item for item in mapped if item["element_type"] == 8 and item["id"] == 127)
        self.assertEqual(material["name"], "女神之泪")
        self.assertIn("无需开箱", material["details"])
        self.assertIn("装备制作所需材料", material["details"])
        self.assertIn("400131", material["details"])

    def test_single_consistent_box_name_becomes_the_material_display_name(self):
        items = [
            {"id": 400045, "element_type": 9, "name": "精炼粉末", "category": "随机箱"},
            {"id": 10, "element_type": 8, "name": "强化袋", "category": "资源"},
        ]
        mapped = dev_mail_grant.map_fixed_boxes_to_direct_items(
            items,
            {400045: (8, 10, 5)},
            {},
        )
        material = next(item for item in mapped if item["element_type"] == 8 and item["id"] == 10)
        self.assertEqual(material["name"], "精炼粉末")
        self.assertIn("原始资源名：强化袋", material["details"])

    def test_localized_names_reads_the_requested_text_namespace(self):
        import sqlite3

        connection = sqlite3.connect(":memory:")
        connection.execute("CREATE TABLE RandomBoxTextTable (id INTEGER, ProtoBuf BLOB)")
        name = "精炼粉末".encode("utf-8")
        connection.execute(
            "INSERT INTO RandomBoxTextTable VALUES (?, ?)",
            (7, b"\x10\x07\x22" + bytes([len(name)]) + name),
        )
        self.assertEqual(dev_mail_grant._localized_names(connection, "RandomBoxTextTable"), {7: "精炼粉末"})
        connection.close()

    def test_internal_lost_resource_is_not_mail_safe(self):
        self.assertFalse(dev_mail_grant._safe_direct_mail_item({
            "id": 90045, "element_type": 8, "name": "金币遗失物品",
            "source_table": "ResourceTable", "resource_type": 2,
        }))

    def test_gold_currency_mail_uses_type_four_id_zero_and_requested_count(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "mail.json"
            output = root / "generated.json"
            source.write_text(json.dumps({
                "version": "2.34.13", "mails": [], "mail_count": 1, "max_mail_id": 0,
            }), encoding="utf-8")
            gold = {"id": 0, "element_type": 4, "name": "金币"}
            store = dev_mail_grant.MailGrantStore(source, output, [gold], 365)
            result = store.grant({"item_id": 0, "element_type": 4, "count": 123456789})
            self.assertEqual(result["mail"]["reward_types"], [4])
            self.assertEqual(result["mail"]["reward_ids"], [0])
            self.assertEqual(result["mail"]["reward_counts"], [123456789])

    def test_packed_varints_accepts_repeated_and_packed_fields(self):
        self.assertEqual(dev_mail_grant.packed_varints({4: [3, b"\x80\x01\x02"]}, 4), [3, 128, 2])
        with self.assertRaises(ValueError):
            dev_mail_grant.packed_varints({4: [b"\x80"]}, 4)

    def test_item_picker_is_collapsible_and_scrolls_its_list(self):
        self.assertIn('<details id=item-picker>', dev_mail_grant.PAGE)
        self.assertIn('<div class=item-list>', dev_mail_grant.PAGE)
        self.assertIn('max-height:min(40vh,28rem)', dev_mail_grant.PAGE)
        self.assertIn('overflow:auto', dev_mail_grant.PAGE)
        self.assertIn("$('item-picker').open=false", dev_mail_grant.PAGE)

    def test_picker_search_includes_random_box_aliases(self):
        self.assertIn("x.category+' '+(x.details||'')", dev_mail_grant.PAGE)

    def test_grant_writes_complete_seed_without_state_mutation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source-mail.json"
            output = root / "generated-mail.json"
            source.write_text(json.dumps({
                "version": "2.34.13",
                "mails": [{
                    "mail_id": 100, "mail_type": 2, "title": "base", "body": "base",
                    "expires_at": 200, "reward_types": [8], "reward_ids": [7],
                    "reward_counts": [1], "sent_at": 100,
                }],
                "mail_count": 2, "max_mail_id": 100,
            }), encoding="utf-8")
            original_source = source.read_text(encoding="utf-8")
            store = dev_mail_grant.MailGrantStore(source, output, [{
                "id": 9, "element_type": 8, "name": "slime",
            }], 365)
            self.assertTrue(output.is_file())
            self.assertEqual(json.loads(output.read_text(encoding="utf-8"))["mail_count"], 2)
            result = store.grant({"item_id": 9, "element_type": 8, "count": 123, "title": "test", "body": "body"})
            written = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(source.read_text(encoding="utf-8"), original_source)
            self.assertEqual(written["mail_count"], 3)
            self.assertEqual(written["max_mail_id"], 101)
            self.assertEqual(written["mails"][-1]["reward_types"], [8])
            self.assertEqual(written["mails"][-1]["reward_ids"], [9])
            self.assertEqual(written["mails"][-1]["reward_counts"], [123])
            self.assertFalse(result["restart_required"])
            with self.assertRaises(ValueError):
                store.grant({"item_id": 999, "element_type": 8, "count": 1})
            with self.assertRaises(ValueError):
                store.grant({"item_id": 9, "element_type": 8, "count": dev_mail_grant.MAX_INT32 + 1})


class ClientSourceToolTests(unittest.TestCase):
    def test_generated_tree_staging_uses_parent_inheriting_directory_mode(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "dev" / "client-source-readable"
            real_mkdir = inherited_stage.os.mkdir
            calls = []

            def recording_mkdir(path, *args, **kwargs):
                calls.append((Path(path), args, kwargs))
                return real_mkdir(path, *args, **kwargs)

            with mock.patch.object(inherited_stage.os, "mkdir", side_effect=recording_mkdir):
                stage = inherited_stage.create_inherited_stage(output)
            try:
                self.assertEqual(stage.parent, output.parent)
                stage_call = next(call for call in calls if call[0] == stage)
                self.assertEqual(stage_call[1], ())
                self.assertEqual(stage_call[2], {})
            finally:
                stage.rmdir()

    def test_deobfuscates_code_not_comments_or_literals_and_records_collisions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "Assembly-CSharp"
            source.mkdir()
            mapping = root / "translation.obfuscate"
            mapping.write_text(
                "#ReverseOrder\n"
                "α⇨Net.Player/User-Info\n"
                "β⇨Other.User Info\n"
                "γ⇨class\n"
                "δ⇨Meaning.D\n",
                encoding="utf-8",
            )
            (source / "α.cs").write_text(
                "// α β γ δ\n"
                "class α { string v = \"α β γ δ\"; char x = 'α'; α f; β g; γ h; δ i; }\n",
                encoding="utf-8",
            )
            (source / "asset.bin").write_bytes(b"\x00a")
            output = root / "mirror"
            manifest = deobfuscate_client_source.deobfuscate(source, mapping, output)
            text = (output / "User_Info.cs").read_text(encoding="utf-8")
            self.assertIn("// α β γ δ", text)
            self.assertIn('"α β γ δ"', text)
            self.assertIn("char x = 'α'", text)
            self.assertIn("class User_Info", text)
            self.assertIn("User_Info__from_u03B2 g", text)
            self.assertIn("_class h", text)
            self.assertIn("D i", text)
            self.assertEqual((output / "asset.bin").read_bytes(), b"\x00a")
            self.assertEqual(manifest["statistics"]["identifier_replacements"], 5)
            self.assertTrue((output / deobfuscate_client_source.MANIFEST).is_file())

    def test_deobfuscator_skips_ambiguous_scoped_symbol(self):
        entries = [
            {"line": 1, "source": "α", "meaning": "One.Value"},
            {"line": 2, "source": "α", "meaning": "Two.Value"},
        ]
        replacements, report, warnings = deobfuscate_client_source.build_replacements(entries)
        self.assertNotIn("α", replacements)
        self.assertEqual(report, [])
        self.assertTrue(any("ambiguous" in warning for warning in warnings))

    def test_deobfuscator_refuses_output_inside_source_or_unmanaged_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            mapping = root / "map"
            mapping.write_text("a⇨Name\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                deobfuscate_client_source.deobfuscate(source, mapping, source / "out")
            output = root / "output"
            output.mkdir()
            (output / "someone.txt").write_text("keep", encoding="utf-8")
            with self.assertRaises(FileExistsError):
                deobfuscate_client_source.deobfuscate(source, mapping, output)

    def test_reconstructs_proto_and_lossless_descriptor_set(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "Assembly-CSharp"
            net = source / "Proto" / "Net"
            net.mkdir(parents=True)
            descriptor = descriptor_pb2.FileDescriptorProto(
                name="Request/Login.proto", package="proto.net", syntax="proto3"
            )
            message = descriptor.message_type.add(name="LoginRequest")
            message.field.add(
                name="seq", number=1,
                label=descriptor_pb2.FieldDescriptorProto.LABEL_OPTIONAL,
                type=descriptor_pb2.FieldDescriptorProto.TYPE_INT32,
            )
            encoded = base64.b64encode(descriptor.SerializeToString()).decode("ascii")
            (net / "LoginRequestReflection.cs").write_text(
                "private static FileDescriptor descriptor = "
                "FileDescriptor.FromGeneratedCode(Convert.FromBase64String("
                f"string.Concat(new string[] {{ \"{encoded}\" }})), new FileDescriptor[0], info);",
                encoding="utf-8",
            )
            output = root / "proto-view"
            manifest = extract_client_proto.reconstruct(source, output)
            proto = (output / "Request" / "Login.proto").read_text(encoding="utf-8")
            self.assertIn('syntax = "proto3";', proto)
            self.assertIn("package proto.net;", proto)
            self.assertIn("message LoginRequest", proto)
            self.assertIn("int32 seq = 1;", proto)
            saved = descriptor_pb2.FileDescriptorSet()
            saved.ParseFromString((output / extract_client_proto.DESCRIPTOR_SET).read_bytes())
            self.assertEqual(saved.file[0], descriptor)
            self.assertEqual(manifest["statistics"]["proto_files"], 1)

    def test_proto_extractor_rejects_unmanaged_or_nested_outputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            (source / "Proto" / "Net").mkdir(parents=True)
            with self.assertRaises(ValueError):
                extract_client_proto.reconstruct(source, source / "view")
            descriptor = descriptor_pb2.FileDescriptorProto(name="X.proto", syntax="proto3")
            encoded = base64.b64encode(descriptor.SerializeToString()).decode("ascii")
            (source / "Proto" / "Net" / "XReflection.cs").write_text(
                "Convert.FromBase64String(string.Concat(new string[] {"
                f"\"{encoded}\"" + "})), new FileDescriptor[0], info);",
                encoding="utf-8",
            )
            output = root / "view"
            output.mkdir()
            with self.assertRaises(FileExistsError):
                extract_client_proto.reconstruct(source, output)


if __name__ == "__main__":
    unittest.main()
