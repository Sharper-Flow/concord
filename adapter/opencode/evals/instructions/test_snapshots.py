import json
import os
import tempfile
import unittest
from pathlib import Path

from . import snapshots


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.role = self.root / "role"
        self.role.write_text("---\nname: synthetic\n---\nRole body.\n", encoding="utf-8")
        self.instruction = self.root / "instructions"
        self.instruction.write_text("Follow synthetic rules.\n", encoding="utf-8")
        self.destination = self.root / "snapshot.json"

    def freeze(self, destination=None):
        return snapshots.freeze(destination or self.destination, self.repo, role_file=self.role,
                                instruction_files=[self.instruction], concord_ref="concord-v1",
                                host_ref="host-v1")

    def test_freezes_and_verifies_original_and_normalized_body(self):
        identity = self.freeze()
        result = snapshots.load(self.destination, self.repo)
        self.assertEqual(result["snapshot_id"], identity)
        self.assertEqual(result["role"], "Role body.\n")
        self.assertEqual(result["instructions"], ["Follow synthetic rules.\n"])
        self.assertEqual(result["role_body_sha256"], "f709360bf72f201b7910437a510311af867af709b0cbf077760466c4d5e4b973")
        with self.assertRaises(FileExistsError):
            self.freeze()

    def test_detects_tampering(self):
        self.freeze()
        os.chmod(self.destination, 0o600)
        data = json.loads(self.destination.read_text(encoding="utf-8"))
        data["role"] = "changed"
        self.destination.write_text(json.dumps(data), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "identity mismatch"):
            snapshots.load(self.destination, self.repo)

    def test_rejects_repository_destination_and_accepts_repository_source(self):
        with self.assertRaisesRegex(ValueError, "outside"):
            self.freeze(self.repo / "snapshot")
        self.assertFalse((self.repo / "snapshot").exists())
        self.role = self.repo / "role.md"
        self.role.write_text("Tracked role.\n", encoding="utf-8")
        self.freeze()
        self.assertEqual(snapshots.load(self.destination, self.repo)["role"], "Tracked role.\n")

    def test_rejects_malformed_snapshot(self):
        content = {"version": 1, "role": "Role body.\n"}
        content["snapshot_id"] = snapshots._digest(snapshots._canonical(content))
        self.destination.write_text(json.dumps(content), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "malformed"):
            snapshots.load(self.destination, self.repo)

    def test_rejects_unterminated_frontmatter(self):
        self.role.write_text("---\nname: x\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "unterminated"):
            self.freeze()
