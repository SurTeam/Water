#!/usr/bin/env python3
"""Build all remote server payloads with a matching, auditable identity."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
TARGETS = [(system, arch) for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")]


def main():
    variant = os.environ.get("WATER_APP_VARIANT", "dev")
    version = os.environ.get("WATER_APP_VERSION", (ROOT / "VERSION").read_text().strip())
    if variant not in ("dev", "release") or not version.strip():
        raise SystemExit("invalid WATER_APP_VARIANT or empty WATER_APP_VERSION")
    revision = subprocess.check_output([sys.executable, str(ROOT / "scripts/server-revision.py")], cwd=ROOT, text=True).strip()
    identity = {"variant": variant, "version": version, "server_revision": revision}
    package = "github.com/SurTeam/Water/internal/gobuild"
    flags = "-s -w " + " ".join("-X " + package + "." + key + "=" + value for key, value in (("Variant", variant), ("Version", version), ("ServerRevision", revision)))
    destination = ROOT / "internal/goremote/payloads"
    destination.mkdir(parents=True, exist_ok=True)
    records = []
    # Publish only after the complete matrix succeeds; a failure retains the old set.
    with tempfile.TemporaryDirectory(prefix="water-payloads-", dir=destination.parent) as staging:
        staging = Path(staging)
        for system, arch in TARGETS:
            name = "water-server-" + system + "-" + arch + (".exe" if system == "windows" else "")
            output = staging / name
            print("building " + system + "/" + arch, flush=True)
            subprocess.run(["go", "build", "-trimpath", "-ldflags", flags, "-o", str(output), "./cmd/water-server"], cwd=ROOT, env=dict(os.environ, CGO_ENABLED="0", GOOS=system, GOARCH=arch), check=True)
            records.append(dict(os=system, arch=arch, file=name, size=output.stat().st_size, sha256=hashlib.sha256(output.read_bytes()).hexdigest()))
        manifest = dict(schema=1, identity=identity, targets=records)
        (staging / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        for record in records:
            os.replace(staging / record["file"], destination / record["file"])
        os.replace(staging / "manifest.json", destination / "manifest.json")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
