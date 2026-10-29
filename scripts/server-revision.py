#!/usr/bin/env python3
"""Fingerprint server dependency sources independently of the GUI/product version."""
import hashlib
from pathlib import Path
import re
import os
import subprocess

root = Path(__file__).resolve().parent.parent
files = {root / "go.mod", root / "go.sum"}
# Hash the union of supported server dependencies, independently of build host.
for system in ("darwin", "linux", "windows"):
    for arch in ("amd64", "arm64"):
        result = subprocess.run(["go", "list", "-deps", "-f", '{{if .Module}}{{if eq .Module.Path "github.com/SurTeam/Water"}}{{.Dir}}{{range .EmbedFiles}}{{printf "\\t%s" .}}{{end}}{{end}}{{end}}', "./cmd/water-server"], cwd=root, env=dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0"), check=True, capture_output=True, text=True)
        for line in result.stdout.splitlines():
            if line.strip():
                directory, *embedded = line.split("\t")
                files.update(p for p in Path(directory).glob("*.go") if not p.name.endswith("_test.go") and not p.name.startswith((".", "_")))
                files.update(Path(directory) / name for name in embedded)
hash_value = hashlib.sha256()
for path in sorted(files, key=lambda item: item.relative_to(root).as_posix()):
    content = path.read_bytes()
    if path == root / "internal/gobuild/identity.go":
        content = re.sub(rb'(\b(?:Version|ServerRevision)\s*=\s*)"[^"]*"', rb'\1"build-override"', content)
    hash_value.update(path.relative_to(root).as_posix().encode() + b"\0" + content + b"\0")
print(hash_value.hexdigest())
