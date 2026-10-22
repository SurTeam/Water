#!/usr/bin/env python3
"""Exercise Metal drawable lifetime across readback pools and frame completion."""
import os
from pathlib import Path
import subprocess
import tempfile
from water_test import GUILifetimeLock

with GUILifetimeLock():
    root = Path(__file__).resolve().parent.parent
    directory = Path(tempfile.mkdtemp(prefix="water-metal-readback.", dir="/tmp"))
    binary = directory / "metal-readback"
    subprocess.run(["go", "build", "-o", str(binary), "./tests/fixtures/metal-readback"],
                   cwd=root, check=True, timeout=30)
    for label, args in (("draw-after-read", []), ("empty-present", ["--empty-present"])):
        with (directory / (label+".log")).open("w") as log:
            # subprocess.run kills and reaps only this owned fixture on timeout.
            subprocess.run([str(binary), *args], cwd=root, env=dict(os.environ),
                           stdout=log, stderr=subprocess.STDOUT, check=True, timeout=10)
        print((directory / (label+".log")).read_text().strip(), flush=True)
    print("artifacts="+str(directory), flush=True)
