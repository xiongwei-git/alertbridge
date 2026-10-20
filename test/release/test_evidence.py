"""Offline regression checks for release evidence and runtime-only OCI export."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("evidence", Path(__file__).resolve().parents[2] / "scripts/release-evidence.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.layout = self.root / "oci"
        (self.layout / "blobs/sha256").mkdir(parents=True)
        self.evidence = self.root / "evidence"
        self.index = {"schemaVersion": 2, "manifests": []}
        for arch in ("amd64", "arm64"):
            manifest = self.blob({"config": self.blob({"os": "linux", "architecture": arch}), "layers": []})
            manifest["platform"] = {"os": "linux", "architecture": arch}
            self.index["manifests"].append(manifest)
            statements = []
            for kind, predicate in (("https://slsa.dev/provenance/v0.2", {"buildType": "https://mobyproject.org/buildkit@v1"}),
                                    ("https://spdx.dev/Document", {"spdxVersion": "SPDX-2.3"})):
                statements.append(self.blob({"predicateType": kind, "predicate": predicate,
                                            "subject": [{"digest": {"sha256": manifest["digest"][7:]}}]}))
            attestation = self.blob({"config": self.blob({}), "layers": statements})
            attestation["platform"] = {"os": "unknown", "architecture": "unknown"}
            attestation["annotations"] = {"vnd.docker.reference.type": "attestation-manifest",
                                          "vnd.docker.reference.digest": manifest["digest"]}
            self.index["manifests"].append(attestation)

    def blob(self, value):
        data = release.encoded(value)
        key = release.digest(data)
        (self.layout / "blobs/sha256" / key[7:]).write_bytes(data)
        return {"digest": key, "size": len(data), "mediaType": "application/vnd.oci.image.manifest.v1+json"}

    def prepare(self, wrapped=False):
        index = self.index
        if wrapped:
            descriptor = self.blob(index)
            descriptor["mediaType"] = "application/vnd.oci.image.index.v1+json"
            index = {"manifests": [descriptor]}
        (self.layout / "index.json").write_bytes(release.encoded(index))
        return release.prepare(self.layout, self.evidence)

    def test_both_platforms_and_evidence_preserved_without_attestations_in_runtime(self):
        expected = copy.deepcopy([d for d in self.index["manifests"] if d["platform"]["os"] == "linux"])
        key = self.prepare(wrapped=True)
        runtime = json.loads((self.evidence / "runtime-index.json").read_bytes())
        self.assertEqual(runtime["manifests"], expected)
        self.assertEqual(len(list(self.evidence.glob("*-sbom.json"))), 2)
        self.assertEqual(len(list(self.evidence.glob("*-provenance.json"))), 2)
        self.assertEqual(release.verify(self.evidence, self.evidence / "runtime-index.json"), key)
        reference = json.loads((self.layout / "index.json").read_bytes())["manifests"][0]
        self.assertEqual(reference["digest"], key)
        self.assertEqual(reference["annotations"]["org.opencontainers.image.ref.name"], "release")

    def test_multiple_tag_references_to_same_build_index(self):
        descriptor = self.blob(self.index)
        descriptor["mediaType"] = "application/vnd.oci.image.index.v1+json"
        aliases = []
        for name in ("v0.6.1", "v0.6", "latest", "latest"):
            alias = copy.deepcopy(descriptor)
            alias["annotations"] = {"org.opencontainers.image.ref.name": name}
            aliases.append(alias)
        (self.layout / "index.json").write_bytes(release.encoded({"manifests": aliases}))
        key = release.prepare(self.layout, self.evidence)
        self.assertEqual(release.verify(self.evidence, self.evidence / "runtime-index.json"), key)
        self.assertEqual(len(list(self.evidence.glob("*-sbom.json"))), 2)
        self.assertEqual(len(list(self.evidence.glob("*-provenance.json"))), 2)

    def test_every_tag_reference_size_is_validated(self):
        descriptor = self.blob(self.index)
        descriptor["mediaType"] = "application/vnd.oci.image.index.v1+json"
        corrupt = copy.deepcopy(descriptor)
        corrupt["size"] += 1
        (self.layout / "index.json").write_bytes(release.encoded({"manifests": [descriptor, corrupt]}))
        with self.assertRaisesRegex(ValueError, "integrity mismatch"):
            release.prepare(self.layout, self.evidence)

    def test_different_build_indexes_are_rejected(self):
        first = self.blob(self.index)
        second = self.blob({"manifests": []})
        for descriptor in (first, second):
            descriptor["mediaType"] = "application/vnd.oci.image.index.v1+json"
        (self.layout / "index.json").write_bytes(release.encoded({"manifests": [first, second]}))
        with self.assertRaisesRegex(ValueError, "different build indexes"):
            release.prepare(self.layout, self.evidence)

    def test_missing_platform_or_attestation_fails(self):
        self.index["manifests"] = self.index["manifests"][:-1]
        with self.assertRaisesRegex(ValueError, "SBOM and provenance required"):
            self.prepare()

    def test_missing_platform_fails(self):
        self.index["manifests"] = self.index["manifests"][:2]
        with self.assertRaisesRegex(ValueError, "both runtime platforms"):
            self.prepare()

    def test_duplicate_platform_fails(self):
        self.index["manifests"].append(self.index["manifests"][0])
        with self.assertRaisesRegex(ValueError, "duplicate runtime"):
            self.prepare()

    def test_wrong_subject_fails(self):
        attestation = self.index["manifests"][1]
        attestation["annotations"]["vnd.docker.reference.digest"] = "sha256:" + "0" * 64
        with self.assertRaisesRegex(ValueError, "matching runtime"):
            self.prepare()

    def replace_subject(self, subjects):
        old = self.index["manifests"][1]
        manifest = json.loads((self.layout / "blobs/sha256" / old["digest"][7:]).read_bytes())
        layer = manifest["layers"][0]
        statement = json.loads((self.layout / "blobs/sha256" / layer["digest"][7:]).read_bytes())
        statement["subject"] = subjects
        manifest["layers"][0] = self.blob(statement)
        replacement = self.blob(manifest)
        replacement.update(platform=old["platform"], annotations=old["annotations"])
        self.index["manifests"][1] = replacement

    def test_statement_subject_must_match_platform_digest(self):
        self.replace_subject([{"digest": {"sha256": "0" * 64}}])
        with self.assertRaisesRegex(ValueError, "subject does not match"):
            self.prepare()

    def test_untagged_export_with_empty_subject_is_rejected(self):
        self.replace_subject([])
        with self.assertRaisesRegex(ValueError, "subject does not match"):
            self.prepare()

    def test_runtime_config_platform_must_match_descriptor(self):
        self.index["manifests"][0]["platform"]["architecture"] = "arm64"
        with self.assertRaisesRegex(ValueError, "config platform mismatch"):
            self.prepare()

    def test_corrupt_blob_fails(self):
        key = self.index["manifests"][0]["digest"][7:]
        (self.layout / "blobs/sha256" / key).write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "integrity mismatch"):
            self.prepare()

    def test_changed_published_digest_fails(self):
        self.prepare()
        remote = self.root / "remote.json"
        remote.write_text('{"manifests":[]}')
        with self.assertRaisesRegex(ValueError, "differs from validated build"):
            release.verify(self.evidence, remote)


if __name__ == "__main__":
    unittest.main()
