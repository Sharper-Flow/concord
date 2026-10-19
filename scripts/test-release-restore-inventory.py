#!/usr/bin/env python3
"""Tests for the release restore inventory (scripts/release-restore-inventory.go).

The real generator runs as a subprocess against a synthetic Git repository
whose tags carry each registry form the released history uses:

- form A composite literals, which name no payload type, are refused and
  marked as the accepted named exclusion;
- registerEventKind[T], registerScopedEventKind[T], and withRecordedSchemas
  entries are probed against the tag's own compiled source;
- any other entry form, and a source that does not compile, is refused
  without being excluded;
- tags with identical inputs share one probe group;
- each written (kind, version, type) binds only witness rows whose payload is
  exactly that encoding, and stays uncovered until its witnesses together
  present every field path.

The fixture module is synthetic; no released Concord source is compiled.
"""

from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import git_environment

git_environment.scrub_inherited()

ROOT = Path(__file__).resolve().parents[1]
GENERATOR = "scripts/release-restore-inventory.go"
UNTYPED = "registry entries name no payload type (form A); accepted named exclusion from shape coverage"

GO_MOD = "module example.com/fixture\n\ngo 1.26\n"

PRELUDE = """package store

import "encoding/json"

type Upcaster func(int) int

type EventKindRegistration struct {
	CurrentVersion  int
	MinSupported    int
	Upcasters       map[int]Upcaster
	RecordedSchemas map[int][]string
}

func registerEventKind[T any](current, minimum int, upcasters map[int]Upcaster) EventKindRegistration {
	return EventKindRegistration{CurrentVersion: current, MinSupported: minimum, Upcasters: upcasters}
}

func registerScopedEventKind[T any](current, minimum int, upcasters map[int]Upcaster) EventKindRegistration {
	return registerEventKind[T](current, minimum, upcasters)
}

func workflowRegistration[T any](current int, upcasters map[int]Upcaster) EventKindRegistration {
	return registerEventKind[T](current, 1, upcasters)
}

func workflowScopedRegistration[T any](current int, upcasters map[int]Upcaster) EventKindRegistration {
	return registerScopedEventKind[T](current, 1, upcasters)
}

func withRecordedSchemas(r EventKindRegistration, schemas map[int][]string) EventKindRegistration {
	r.RecordedSchemas = schemas
	return r
}

type Inner struct {
	Depth int `json:"depth"`
}

type hidden struct {
	Promoted string `json:"promoted"`
}

type Stamp struct{}

func (Stamp) MarshalJSON() ([]byte, error) { return []byte("0"), nil }

type Node struct {
	Next *Node `json:"next,omitempty"`
}

type Conflict struct {
	Inner
	Depth int `json:"depth"`
}

type Tree []Tree
type Dict map[string]Dict
type Key int
func (Key) MarshalText() ([]byte, error) { return []byte("key"), nil }
type NullableEmbedding struct { *Inner }
type Containers struct {
	Slice []string `json:"slice"`
	Array [2]string `json:"array"`
	Tree Tree `json:"tree"`
	Dict Dict `json:"dict"`
	Keys map[Key]int `json:"keys"`
	Number json.Number `json:"number"`
}

type Typed struct {
	hidden
	Name     string            `json:"name"`
	Count    int64             `json:"count,string"`
	Optional *Inner            `json:"optional,omitempty"`
	Tags     []string          `json:"tags"`
	Labels   map[string]Inner  `json:"labels"`
	Raw      json.RawMessage   `json:"raw"`
	When     Stamp             `json:"when"`
	Any      any               `json:"any"`
	Blob     []byte            `json:"blob"`
	Skipped  string            `json:"-"`
	Default  bool
	private  int
	Chain    Node              `json:"chain"`
}
"""

TYPED_REGISTRY = """
const ThingConstant = "thing.constant"

var eventKindRegistry = map[string]EventKindRegistration{
	ThingConstant:     registerEventKind[Inner](1, 1, nil),
	"thing.typed":     registerEventKind[Typed](2, 1, map[int]Upcaster{1: nil}),
	"thing.scoped":    registerScopedEventKind[Inner](1, 1, nil),
	"thing.recorded":  withRecordedSchemas(registerEventKind[Inner](2, 1, map[int]Upcaster{1: nil}), map[int][]string{1: nil}),
	"thing.conflict":  registerEventKind[Conflict](1, 1, nil),
	"flow.plain":      workflowRegistration[Inner](3, nil),
	"flow.scoped":     workflowScopedRegistration[Stamp](1, nil),
	"thing.containers": registerEventKind[Containers](1, 1, nil),
	"thing.nullable_embedding": registerEventKind[NullableEmbedding](1, 1, nil),
}
"""

FORM_A_REGISTRY = """
var eventKindRegistry = map[string]EventKindRegistration{
	"thing.typed": {CurrentVersion: 1, MinSupported: 1},
}
"""

UNKNOWN_FORM_REGISTRY = """
func build() EventKindRegistration { return EventKindRegistration{} }

var eventKindRegistry = map[string]EventKindRegistration{
	"thing.typed": build(),
}
"""

BROKEN_REGISTRY = TYPED_REGISTRY + "\nvar broken int = \"not an int\"\n"
LATE_KIND_REGISTRY = TYPED_REGISTRY + "\nfunc init() { eventKindRegistry[\"thing.late\"] = registerEventKind[Inner](1, 1, nil) }\n"

TYPED_BASE = {"promoted": "p", "name": "n", "count": "3", "tags": ["a"], "labels": {"k": {"depth": 1}}, "raw": {}, "when": 0, "any": 1, "blob": "AA==", "Default": True, "chain": {}}
TYPED_FULL = TYPED_BASE | {"optional": {"depth": 2}, "chain": {"next": {}}}


def witness(test: str, kind: str, version: int, payload: object) -> dict:
    return {"test": test, "kind": kind, "payload_version": version, "payload": json.dumps(payload)}


WITNESSES = [
    witness("TestConstant", "thing.constant", 1, {"depth": 1}),
    witness("TestRecordedOld", "thing.recorded", 1, {"depth": 1}),
    witness("TestRecordedExtra", "thing.recorded", 2, {"depth": 1, "extra": 1}),
    witness("TestScopedMissing", "thing.scoped", 1, {}),
    witness("TestTypedBase", "thing.typed", 2, TYPED_BASE),
    witness("TestTypedFull", "thing.typed", 2, TYPED_FULL),
    witness("TestTypedCountNumber", "thing.typed", 2, TYPED_BASE | {"count": 3}),
]


def run(args: list[str], cwd: Path, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, check=False, timeout=300)


class InventoryTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.scratch = Path(tempfile.mkdtemp(prefix="release-restore-inventory-test-"))
        cls.binary = cls.scratch / "inventory"
        built = run(["go", "build", "-o", str(cls.binary), GENERATOR], ROOT)
        if built.returncode != 0:
            raise AssertionError(built.stderr)
        cls.repo = cls.scratch / "repo"
        cls.repo.mkdir()
        git = lambda *args: run(["git", *args], cls.repo)
        for command in (["init", "-q"], ["config", "user.email", "fixture@example.invalid"], ["config", "user.name", "fixture"]):
            assert git(*command).returncode == 0
        (cls.repo / "go.mod").write_text(GO_MOD)
        (cls.repo / "go.sum").write_text("")
        store = cls.repo / "internal" / "store"
        store.mkdir(parents=True)
        # (tag, registry, annotated); v1.0.1 differs outside internal/ only.
        for tag, registry, annotated in (
            ("v0.1.0", FORM_A_REGISTRY, False),
            ("v1.0.0", TYPED_REGISTRY, True),
            ("v1.0.1", TYPED_REGISTRY, False),
            ("v1.0.1-rc.1", TYPED_REGISTRY, False),
            ("v2.0.0", UNKNOWN_FORM_REGISTRY, False),
            ("v3.0.0", BROKEN_REGISTRY, False),
            ("v4.0.0", FORM_A_REGISTRY, False),
            ("v0.2.0", FORM_A_REGISTRY.replace('"thing.typed": {CurrentVersion: 1, MinSupported: 1},', '"thing.typed": {CurrentVersion: 1, MinSupported: 1}, "mixed": registerEventKind[Inner](1, 1, nil),'), False),
            ("v5.0.0", LATE_KIND_REGISTRY, False),
        ):
            (store / "operation.go").write_text(PRELUDE + registry)
            (cls.repo / "README").write_text(tag)
            assert git("add", "-A").returncode == 0
            assert git("commit", "-q", "-m", tag).returncode == 0
            assert git("tag", *(["-a", "-m", tag] if annotated else []), tag).returncode == 0
        cls.first, cls.coverage = cls.generate("first", WITNESSES)

    @classmethod
    def tearDownClass(cls) -> None:
        shutil.rmtree(cls.scratch, ignore_errors=True)

    @classmethod
    def generate(cls, name: str, witnesses: list[dict]) -> tuple[dict, dict]:
        output, coverage, log = (cls.scratch / f"{name}.{suffix}" for suffix in ("writers.json", "coverage.json", "witnesses.jsonl"))
        log.write_text("".join(json.dumps(w) + "\n" for w in witnesses))
        result = run([str(cls.binary), "-out", str(output), "-coverage", str(coverage), "-witnesses", str(log)], cls.repo)
        if result.returncode != 1 or "inventory held" not in result.stderr or "coverage held" not in result.stderr:
            raise AssertionError((result.returncode, result.stdout, result.stderr))
        return json.loads(output.read_text()), json.loads(coverage.read_text())

    def shapes(self, coverage: dict | None = None) -> dict[tuple[str, int], dict]:
        return {(s["kind"], s["payload_version"]): s for s in (coverage or self.coverage)["shapes"]}

    def rows(self) -> dict[str, dict]:
        return {row["tag"]: row for row in self.first["rows"]}

    def shape(self, kind: str, tag: str = "v1.0.0") -> dict:
        registration = next(r for r in self.first["groups"][self.rows()[tag]["group"]] if r["kind"] == kind)
        return self.first["types"][registration["type"]]

    def test_population_is_every_strict_tag_in_version_order(self) -> None:
        self.assertEqual([row["tag"] for row in self.first["rows"]], ["v0.1.0", "v0.2.0", "v1.0.0", "v1.0.1", "v2.0.0", "v3.0.0", "v4.0.0", "v5.0.0"])
        self.assertEqual(self.first["schema"], "concord.release-restore-writers.v1")
        self.assertEqual(self.first["generator"], "go run scripts/release-restore-inventory.go")

    def test_annotated_tag_keeps_tag_object_and_peeled_commit(self) -> None:
        row = self.rows()["v1.0.0"]
        self.assertNotEqual(row["tag_object"], row["commit"])
        commit = run(["git", "rev-parse", "v1.0.0^{commit}"], self.repo).stdout.strip()
        self.assertEqual(row["commit"], commit)
        light = self.rows()["v1.0.1"]
        self.assertEqual(light["tag_object"], light["commit"])

    def test_form_a_is_the_named_exclusion(self) -> None:
        row = self.rows()["v0.1.0"]
        self.assertEqual((row["status"], row.get("excluded"), row["detail"]), ("inventory_refused", True, UNTYPED))
        self.assertNotIn("group", row)

    def test_only_the_approved_form_a_population_is_excluded(self) -> None:
        for tag in ("v0.2.0", "v4.0.0"):
            self.assertEqual(self.rows()[tag]["status"], "inventory_refused")
            self.assertNotIn("excluded", self.rows()[tag])

    def test_approved_exclusions_do_not_hold(self) -> None:
        repo = self.scratch / "approved"
        repo.mkdir()
        self.assertEqual(run(["git", "init", "-q"], repo).returncode, 0)
        fetched = run(["git", "fetch", "-q", str(self.repo), "refs/tags/v0.1.0:refs/tags/v0.1.0", "refs/tags/v1.0.0:refs/tags/v1.0.0"], repo)
        self.assertEqual(fetched.returncode, 0, fetched.stderr)
        output, log = self.scratch / "approved.json", self.scratch / "approved.jsonl"
        log.write_text("")
        result = run([str(self.binary), "-out", str(output), "-coverage", str(self.scratch / "approved.cov"), "-witnesses", str(log)], repo)
        # Only the unwitnessed v1.0.0 shapes hold; the exclusion does not.
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("coverage held", result.stderr)
        self.assertNotIn("inventory held", result.stderr)
        rows = {row["tag"]: row for row in json.loads(output.read_text())["rows"]}
        self.assertEqual(list(rows), ["v0.1.0", "v1.0.0"])
        self.assertEqual((rows["v0.1.0"]["status"], rows["v0.1.0"]["excluded"]), ("inventory_refused", True))
        self.assertEqual(rows["v1.0.0"]["status"], "inventoried")

    def test_source_census_evidence_is_recorded_for_refused_writers(self) -> None:
        for tag, row in self.rows().items():
            source = run(["git", "show", f"{tag}:internal/store/operation.go"], self.repo).stdout.encode()
            self.assertEqual(row["registry_source"], "sha256:" + hashlib.sha256(source).hexdigest())

    def test_unrecognized_entry_refuses_without_exclusion(self) -> None:
        row = self.rows()["v2.0.0"]
        self.assertEqual(row["status"], "inventory_refused")
        self.assertNotIn("excluded", row)
        self.assertIn('"thing.typed" has an unrecognized form', row["detail"])

    def test_compile_failure_refuses_without_exclusion(self) -> None:
        row = self.rows()["v3.0.0"]
        self.assertEqual(row["status"], "inventory_refused")
        self.assertNotIn("excluded", row)
        self.assertIn("probe ", row["detail"])
        self.assertNotIn(row["group"], self.first["groups"])

    def test_kind_outside_the_parsed_registry_refuses(self) -> None:
        row = self.rows()["v5.0.0"]
        self.assertEqual(row["status"], "inventory_refused")
        self.assertNotIn("excluded", row)
        self.assertIn("probe registry has 10 kinds, parsed 9", row["detail"])

    def test_identical_inputs_share_one_group(self) -> None:
        rows = self.rows()
        self.assertEqual(rows["v1.0.0"]["status"], "inventoried")
        self.assertEqual(rows["v1.0.0"]["group"], rows["v1.0.1"]["group"])
        self.assertEqual(set(self.first["groups"]), {rows["v1.0.0"]["group"]})

    def test_registry_metadata_comes_from_the_compiled_registry(self) -> None:
        group = {r["kind"]: r for r in self.first["groups"][self.rows()["v1.0.0"]["group"]]}
        self.assertEqual(sorted(group), ["flow.plain", "flow.scoped", "thing.conflict", "thing.constant", "thing.containers", "thing.nullable_embedding", "thing.recorded", "thing.scoped", "thing.typed"])
        self.assertEqual(group["thing.constant"]["payload_type"], "Inner")
        self.assertEqual(group["thing.typed"]["payload_type"], "Typed")
        self.assertEqual((group["flow.plain"]["current_version"], group["flow.scoped"]["payload_type"]), (3, "Stamp"))
        self.assertEqual((group["thing.typed"]["current_version"], group["thing.typed"]["upcasters"]), (2, [1]))
        self.assertEqual(group["thing.recorded"]["recorded_schemas"], [1])
        self.assertEqual(group["thing.scoped"]["recorded_schemas"], [])
        self.assertEqual(group["thing.scoped"]["payload_type"], "Inner")
        self.assertEqual(group["thing.scoped"]["type"], group["thing.recorded"]["type"])

    def test_descriptor_follows_encoding_json(self) -> None:
        shape = self.shape("thing.typed")
        self.assertEqual(shape["kind"], "object")
        fields = {field["name"]: field for field in shape["fields"]}
        self.assertEqual(list(fields), ["promoted", "name", "count", "optional", "tags", "labels", "raw", "when", "any", "blob", "Default", "chain"])
        self.assertTrue(fields["count"]["string"])
        self.assertTrue(fields["optional"]["omitempty"])
        self.assertEqual(fields["optional"]["shape"], {"kind": "pointer", "elem": {"kind": "object", "fields": [{"name": "depth", "shape": {"kind": "int"}}]}})
        self.assertEqual(fields["tags"]["shape"], {"kind": "slice", "elem": {"kind": "string"}})
        self.assertEqual(fields["labels"]["shape"]["kind"], "map")
        self.assertEqual(fields["labels"]["shape"]["key"], {"kind": "string"})
        self.assertEqual(fields["raw"]["shape"], {"kind": "custom", "type": "json.RawMessage"})
        self.assertEqual(fields["when"]["shape"], {"kind": "custom", "type": "store.Stamp"})
        self.assertEqual(fields["any"]["shape"], {"kind": "custom", "type": "interface {}"})
        self.assertEqual(fields["blob"]["shape"], {"kind": "bytes"})
        chain = fields["chain"]["shape"]["fields"][0]["shape"]
        self.assertEqual(chain, {"kind": "pointer", "elem": {"kind": "recursive", "type": "store.Node"}})

    def test_types_are_content_addressed(self) -> None:
        for key, shape in self.first["types"].items():
            compact = json.dumps(shape, separators=(",", ":"), sort_keys=True).encode()
            self.assertEqual(key, "sha256:" + hashlib.sha256(compact).hexdigest())

    def test_ambiguous_embedding_is_custom(self) -> None:
        self.assertEqual(self.shape("thing.conflict"), {"kind": "custom", "type": "store.Conflict", "reason": "ambiguous or nullable embedded fields"})

    def test_coverage_lists_every_written_shape_once(self) -> None:
        kinds = {r["kind"]: r["current_version"] for r in self.first["groups"][self.rows()["v1.0.0"]["group"]]}
        self.assertEqual(set(self.shapes()), set(kinds.items()))
        self.assertEqual(self.coverage["schema"], "concord.release-restore-coverage.v1")
        self.assertEqual(self.shapes()[("thing.constant", 1)]["writers"], ["v1.0.0", "v1.0.1"])

    def test_exact_witness_binds_a_shape(self) -> None:
        constant = self.shapes()[("thing.constant", 1)]
        self.assertEqual((constant["covered"], constant["witnesses"]), (True, ["TestConstant"]))
        self.assertNotIn("missing_paths", constant)

    def test_witnesses_together_present_every_path(self) -> None:
        typed = self.shapes()[("thing.typed", 2)]
        self.assertEqual((typed["covered"], typed["witnesses"]), (True, ["TestTypedBase", "TestTypedFull"]))

    def test_nonconforming_witnesses_bind_nothing(self) -> None:
        # Wrong version, an unknown member, and a missing always-emitted member.
        for key in (("thing.recorded", 2), ("thing.scoped", 1), ("flow.plain", 3)):
            shape = self.shapes()[key]
            self.assertEqual((shape["covered"], shape["witnesses"]), (False, []), key)
            self.assertIn("$", shape["missing_paths"])

    def test_partial_witnesses_name_the_missing_paths(self) -> None:
        _, coverage = self.generate("partial", [WITNESSES[4]])
        typed = self.shapes(coverage)[("thing.typed", 2)]
        self.assertEqual((typed["covered"], typed["witnesses"]), (False, ["TestTypedBase"]))
        self.assertEqual(typed["missing_paths"], ["$.chain.next", "$.optional", "$.optional.depth"])

    def test_witness_log_is_required(self) -> None:
        result = run([str(self.binary), "-out", str(self.scratch / "none.json")], self.repo)
        self.assertEqual(result.returncode, 1)
        self.assertIn("-witnesses is required", result.stderr)

    def test_malformed_witness_log_refuses(self) -> None:
        log = self.scratch / "malformed.jsonl"
        log.write_text("not json\n")
        result = run([str(self.binary), "-out", str(self.scratch / "m.json"), "-coverage", str(self.scratch / "m.cov"), "-witnesses", str(log)], self.repo)
        self.assertEqual(result.returncode, 1)
        self.assertIn("is not a witness row", result.stderr)

    def test_container_shapes_preserve_wire_distinctions(self) -> None:
        fields = {f["name"]: f["shape"] for f in self.shape("thing.containers")["fields"]}
        self.assertEqual(fields["slice"], {"kind": "slice", "elem": {"kind": "string"}})
        self.assertEqual(fields["array"], {"kind": "array", "length": 2, "elem": {"kind": "string"}})
        self.assertEqual(fields["tree"], {"kind": "slice", "elem": {"kind": "recursive", "type": "store.Tree"}})
        self.assertEqual(fields["dict"]["elem"], {"kind": "recursive", "type": "store.Dict"})
        self.assertEqual(fields["keys"]["key"], {"kind": "custom", "type": "store.Key"})
        self.assertEqual(fields["number"], {"kind": "custom", "type": "json.Number"})
        self.assertEqual(self.shape("thing.nullable_embedding")["kind"], "custom")

if __name__ == "__main__":
    unittest.main(verbosity=2)
