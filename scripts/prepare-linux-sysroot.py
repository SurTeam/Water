#!/usr/bin/env python3
"""Prepare an Alpine dynamic-musl cross sysroot; never run target code.

Usage: python3 scripts/prepare-linux-sysroot.py amd64|arm64
Requires Python 3.12+, Zig and host pkg-config. APK URLs/versions/SHA256 are
pinned in linux-musl-packages.json. No apk installation or source build occurs.
Source target/linux-musl-dynamic/<arch>/env.sh before build-go-linux.sh.
WATER_LINUX_SYSROOT_CACHE can relocate the generated sysroots and downloads.
The resulting application requires the matching Alpine musl/X11/Mesa runtime.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tarfile
import urllib.request


COMPILER = '''here="$(cd "$(dirname "$0")" && pwd)"
root="$here/root"
args=()
link=yes
shared=no
crt=crt1.o
for arg in "$@"; do
  case "$arg" in
    -c|-E|-S|-fsyntax-only|-M|-MM|-dump*|-print*|--version) link=no ;;
    -shared) shared=yes ;;
    -pie) crt=Scrt1.o ;;
    -static|-static-pie) echo "error: this toolchain requires dynamic musl" >&2; exit 1 ;;
    -lc|-lpthread|-ldl|-lm|-lrt|-lutil|-lresolv|-lxnet) continue ;;
    -L"$root/"*) arg="-L/${arg#-L$root/}" ;;
  esac
  args+=("$arg")
done
# Zig treats -lc/-lpthread/etc as a request for its bundled libc. Omit those
# aliases and pass Alpine's actual dynamic libc by filename instead. -nostdlib
# then suppresses all implicit CRT/libc selection; Alpine CRT is explicit.
base=(-target @TRIPLE@ -dynamic -nostdlib --sysroot="$root"
      -isystem "$root/usr/include" -L/usr/lib -L/lib)
if [[ "$link" == no ]]; then
  exec "${WATER_ZIG:-zig}" @COMPILER@ "${base[@]}" "${args[@]}"
fi
start=()
if [[ "$shared" == no ]]; then start+=("$root/usr/lib/$crt"); fi
exec "${WATER_ZIG:-zig}" @COMPILER@ "${base[@]}" \
  "${start[@]}" "$root/usr/lib/crti.o" -Wl,--as-needed \
  "${args[@]}" -x none "$root/lib/@LOADER@" "$root/usr/lib/crtn.o" \
  -Wl,--dynamic-linker=/lib/@LOADER@ -Wl,--allow-shlib-undefined
'''


def executable(path, text):
    path.write_text('#!/usr/bin/env bash\nset -euo pipefail\n' + text)
    path.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("arch", choices=("amd64", "arm64"))
    args = parser.parse_args()
    for tool in ("zig", "pkg-config"):
        if not shutil.which(tool):
            parser.error(f"required host tool missing: {tool}")
    repo = Path(__file__).resolve().parent.parent
    lock = json.loads((Path(__file__).parent / "linux-musl-packages.json").read_text())
    machine = {"amd64": "x86_64", "arm64": "aarch64"}[args.arch]
    packages = lock["packages"][machine]
    cache = Path(os.environ.get("WATER_LINUX_SYSROOT_CACHE", repo / "target/linux-musl-dynamic")).resolve()
    output = cache / args.arch
    root = output / "root"
    downloads = cache / "downloads" / machine
    downloads.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    fingerprint = hashlib.sha256(json.dumps(packages, sort_keys=True).encode()).hexdigest()
    stamp = output / "packages.sha256"
    if not stamp.exists() or stamp.read_text() != fingerprint or not root.exists():
        stamp.unlink(missing_ok=True)
        if root.exists():
            shutil.rmtree(root)
        root.mkdir()
        for package in packages:
            archive = downloads / Path(package["url"]).name
            if not archive.exists():
                print(f"Downloading {package['url']}", flush=True)
                temporary = archive.with_suffix(f".{os.getpid()}.part")
                with urllib.request.urlopen(package["url"], timeout=120) as response, temporary.open("wb") as target:
                    shutil.copyfileobj(response, target)
                temporary.replace(archive)
            if hashlib.sha256(archive.read_bytes()).hexdigest() != package["sha256"]:
                raise ValueError(f"SHA256 mismatch: {archive}")
            # APK v2 consists of concatenated gzip/tar streams. Ignore archive
            # terminators to reach the data stream, excluding install scripts.
            with tarfile.open(archive, ignore_zeros=True) as bundle:
                def member_filter(member, destination):
                    member.name = member.name.removeprefix("./")
                    if member.name.startswith("."):
                        return None
                    if member.issym() and member.linkname.startswith("/"):
                        member.linkname = os.path.relpath(root / member.linkname.lstrip("/"), root / Path(member.name).parent)
                    return tarfile.data_filter(member, destination)
                bundle.extractall(root, filter=member_filter)
        # Prevent accidental fallback to the target's static libc archive.
        (root / "usr/lib/libc.a").unlink(missing_ok=True)
        stamp.write_text(fingerprint)
    triple = f"{machine}-linux-musl"
    loader = f"ld-musl-{machine}.so.1"
    for name, compiler in (("cc", "cc"), ("c++", "c++")):
        executable(output / name, COMPILER.replace("@TRIPLE@", triple).replace("@COMPILER@", compiler).replace("@LOADER@", loader))
    executable(output / "pkg-config",
               'here="$(cd "$(dirname "$0")" && pwd)"\n'
               'export PKG_CONFIG_SYSROOT_DIR="$here/root"\n'
               'export PKG_CONFIG_LIBDIR="$here/root/usr/lib/pkgconfig:$here/root/usr/share/pkgconfig"\n'
               'unset PKG_CONFIG_PATH\n'
               'exec "${WATER_HOST_PKG_CONFIG:-pkg-config}" "$@"\n')
    # Only CRT and compiler runtime code is embedded; X11/Mesa are system shared
    # dependencies. Preserve the musl/Zig notices along with package provenance.
    notices = root / "usr/share/water-musl-licenses"
    notices.mkdir(parents=True, exist_ok=True)
    zig_environment = subprocess.check_output(["zig", "env"], text=True)
    match = re.search(r'(?:"lib_dir"|\.lib_dir)\s*[:=]\s*"([^\"]+)"', zig_environment)
    if not match:
        raise ValueError("cannot locate Zig libc notices")
    zig_lib = Path(match.group(1))
    shutil.copyfile(zig_lib / "libc/musl/COPYRIGHT", notices / "musl-COPYRIGHT")
    zig_exe = Path(shutil.which("zig")).resolve()
    zig_license = next(candidate for candidate in
                       (zig_exe.parent.parent / "LICENSE", zig_lib.parent / "LICENSE", zig_lib.parent.parent / "LICENSE")
                       if candidate.is_file())
    shutil.copyfile(zig_license, notices / "zig-LICENSE")
    (notices / "alpine-packages.json").write_text(json.dumps(packages, indent=2) + "\n")
    (output / "env.sh").write_text(
        f'export GOARCH={args.arch}\nexport WATER_LINUX_LIBC=musl\n'
        f'export WATER_LINUX_SYSROOT={shlex.quote(str(root))}\n'
        f'export CC={shlex.quote(str(output / "cc"))}\n'
        f'export CXX={shlex.quote(str(output / "c++"))}\n'
        f'export PKG_CONFIG={shlex.quote(str(output / "pkg-config"))}\n'
        'unset CGO_LDFLAGS ZIG_LIBC\n')
    print(f"Prepared Alpine {lock['alpine']} dynamic musl for {triple}")
    print(f"source {shlex.quote(str(output / 'env.sh'))}")


if __name__ == "__main__":
    main()
