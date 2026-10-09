#!/usr/bin/env python3
"""Fingerprint server dependency sources independently of the GUI/product version."""
import hashlib
from pathlib import Path
import re
import subprocess

root = Path(__file__).resolve().parent.parent
result = subprocess.run(["go", "list", "-deps", "-f", '{{if .Module}}{{if eq .Module.Path "github.com/SurTeam/Water"}}{{.Dir}}{{range .EmbedFiles}}{{printf "\\t%s" .}}{{end}}{{end}}{{end}}', "./cmd/water-server"], cwd=root, check=True, capture_output=True, text=True)
files = {root / "go.mod", root / "go.sum"}
for line in result.stdout.splitlines():
    if line.strip():
        directory, *embedded = line.split("\t")
        files.update(p for p in Path(directory).glob("*.go") if not p.name.endswith("_test.go"))
        files.update(Path(directory) / name for name in embedded)
hash_value = hashlib.sha256()
for path in sorted(files):
    content = path.read_bytes()
    if path == root / "internal/gobuild/identity.go":
        content = re.sub(rb'(\b(?:Version|ServerRevision)\s*=\s*)"[^"]*"', rb'\1"build-override"', content)
    hash_value.update(str(path.relative_to(root)).encode() + b"\0" + content + b"\0")
print(hash_value.hexdigest())
