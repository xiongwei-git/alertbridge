#!/usr/bin/env python3
"""Validate one BuildKit OCI build and separate registry images from evidence."""
import hashlib
import json
from pathlib import Path
import re
import sys


def encoded(value):
    return json.dumps(value, separators=(",", ":")).encode()


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def prepare(layout, evidence):
    evidence.mkdir(parents=True, exist_ok=False)

    def blob(descriptor):
        key = descriptor["digest"]
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", key):
            raise ValueError("invalid blob digest")
        data = (layout / "blobs" / "sha256" / key[7:]).read_bytes()
        if digest(data) != key or len(data) != descriptor["size"]:
            raise ValueError("blob integrity mismatch")
        return data

    index = json.loads((layout / "index.json").read_bytes())
    # A tagged OCI export can contain several aliases of the same build index.
    # Validate every alias before unwrapping; never choose between different builds.
    while any("index" in item["mediaType"] for item in index["manifests"]):
        references = index["manifests"]
        if not all("index" in item["mediaType"] for item in references):
            raise ValueError("mixed build index and manifest references")
        if len({item["digest"] for item in references}) != 1:
            raise ValueError("different build indexes in OCI export")
        for item in references:
            data = blob(item)
        index = json.loads(data)
    (evidence / "build-index.json").write_bytes(encoded(index))
    runtime = {}
    attestations = []
    for descriptor in index["manifests"]:
        manifest = json.loads(blob(descriptor))
        blob(manifest["config"])
        for layer in manifest["layers"]:
            blob(layer)
        platform = descriptor.get("platform", {})
        arch = platform.get("architecture")
        if platform.get("os") == "linux" and arch in ("amd64", "arm64"):
            if arch in runtime:
                raise ValueError("duplicate runtime platform")
            config = json.loads(blob(manifest["config"]))
            if config.get("os") != "linux" or config.get("architecture") != arch:
                raise ValueError("runtime config platform mismatch")
            runtime[arch] = descriptor
        elif descriptor.get("annotations", {}).get("vnd.docker.reference.type") == "attestation-manifest":
            attestations.append((descriptor, manifest))
        else:
            raise ValueError("unexpected build platform or manifest")
    if set(runtime) != {"amd64", "arm64"}:
        raise ValueError("both runtime platforms are required")
    found = {key: set() for key in runtime}
    for descriptor, manifest in attestations:
        reference = descriptor["annotations"].get("vnd.docker.reference.digest")
        arch = next((key for key, value in runtime.items() if value["digest"] == reference), None)
        if arch is None:
            raise ValueError("attestation has no matching runtime image")
        for layer in manifest["layers"]:
            statement = json.loads(blob(layer))
            if not any(subject.get("digest", {}).get("sha256") == reference[7:]
                       for subject in statement.get("subject", [])):
                raise ValueError("attestation subject does not match runtime digest")
            kind = statement.get("predicateType", "")
            predicate = statement.get("predicate", {})
            if kind.startswith("https://slsa.dev/provenance/"):
                # BuildKit mode=max includes complete build parameters/materials.
                if not predicate.get("buildType") and not predicate.get("buildDefinition", {}).get("buildType"):
                    raise ValueError("missing provenance build type")
                label = "provenance"
            elif kind == "https://spdx.dev/Document" and predicate.get("spdxVersion", "").startswith("SPDX-"):
                label = "sbom"
            else:
                raise ValueError("unexpected or malformed attestation predicate")
            if label in found[arch]:
                raise ValueError("duplicate attestation predicate")
            found[arch].add(label)
            (evidence / f"{arch}-{label}.json").write_bytes(encoded(statement))
    if any(kinds != {"provenance", "sbom"} for kinds in found.values()):
        raise ValueError("SBOM and provenance required for each platform")
    filtered = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
                "manifests": [runtime[arch] for arch in sorted(runtime)]}
    data = encoded(filtered)
    key = digest(data)
    (layout / "blobs" / "sha256" / key[7:]).write_bytes(data)
    reference = {"mediaType": filtered["mediaType"], "digest": key, "size": len(data),
                 "annotations": {"org.opencontainers.image.ref.name": "release"}}
    (layout / "index.json").write_bytes(encoded({"schemaVersion": 2, "manifests": [reference]}))
    (evidence / "runtime-index.json").write_bytes(data)
    (evidence / "digest.txt").write_text(key + "\n")
    return key


def verify(evidence, remote):
    expected = (evidence / "digest.txt").read_text().strip()
    if digest(remote.read_bytes()) != expected:
        raise ValueError("published index differs from validated build")
    return expected


if __name__ == "__main__":
    try:
        mode, first, second = sys.argv[1:]
        if mode == "prepare":
            result = prepare(Path(first), Path(second))
        elif mode == "verify":
            result = verify(Path(first), Path(second))
        else:
            raise ValueError("expected prepare or verify")
        print(result)
    except (ValueError, KeyError, OSError, json.JSONDecodeError) as error:
        sys.exit(f"Release evidence validation failed: {error}")
