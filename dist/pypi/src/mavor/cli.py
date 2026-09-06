"""Thin wrapper that fetches and runs the mavor Go binary.

The wheel carries no binary of its own: it resolves the platform, downloads
the matching release archive on first run, and caches it beside this module.
That keeps one small pure-Python wheel on PyPI instead of one per platform.
"""

import platform
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.error
import urllib.request
from importlib.metadata import PackageNotFoundError
from importlib.metadata import version as pkg_version
from pathlib import Path

REPO = "mschulkind-oss/mavor"
BIN_DIR = Path(__file__).parent / "bin"
BIN_NAME = "mavor"

# mavor is a cgo program: the binary links sherpa-onnx and will not start
# without these beside it. The release archive is flat — binary and shared
# objects at the root — and the binary's RUNPATH begins with $ORIGIN, so
# unpacking all three into BIN_DIR is the whole of the install.
LIB_NAMES = ("libonnxruntime.so", "libsherpa-onnx-c-api.so")


def _target() -> tuple[str, str]:
    """Resolve the release archive's os/arch, or exit explaining why not.

    mavor is Linux-only today: the overlay's shared-memory buffers are memfd,
    so there is no darwin or windows build to download. Saying that plainly
    beats a 404 from the release URL.
    """
    system = platform.system().lower()
    machine = platform.machine().lower()

    if system != "linux":
        sys.exit(
            f"mavor has no {system} build: it needs a Wayland compositor, and "
            "the overlay is built on Linux shared memory. Linux only, for now."
        )

    # amd64 only. mavor became a cgo program, and cgo cannot cross-compile to
    # arm64 from the amd64 release runner, so no arm64 archive is published —
    # see the goarch comment in .goreleaser.yaml. An arm64 user builds from
    # source until that is resolved, so say that rather than 404ing.
    arch = {"x86_64": "amd64", "amd64": "amd64"}.get(machine)
    if arch is None:
        sys.exit(
            f"mavor publishes no {machine} build: it links sherpa-onnx through cgo, "
            "which the amd64 release builder cannot cross-compile. Build from "
            f"source instead: https://github.com/{REPO}"
        )

    return "linux", arch


def _binary() -> Path:
    return BIN_DIR / BIN_NAME


def _download() -> Path:
    try:
        ver = pkg_version("mavor")
    except PackageNotFoundError:
        sys.exit("mavor's version is unknown, so the matching release cannot be found.")

    goos, goarch = _target()
    # Must match the archives name_template in .goreleaser.yaml.
    archive = f"mavor_{ver}_{goos}_{goarch}.tar.gz"
    url = f"https://github.com/{REPO}/releases/download/v{ver}/{archive}"

    BIN_DIR.mkdir(parents=True, exist_ok=True)
    print(f"downloading mavor v{ver} for {goos}/{goarch}...", file=sys.stderr)

    with tempfile.TemporaryDirectory() as tmp:
        local = Path(tmp) / archive
        try:
            urllib.request.urlretrieve(url, local)
        except urllib.error.HTTPError as e:
            sys.exit(f"could not download {url}: HTTP {e.code}")
        except urllib.error.URLError as e:
            sys.exit(f"could not download {url}: {e.reason}")

        with tarfile.open(local, "r:gz") as tar:
            # Extract the binary and its shared objects by name, and nothing
            # else. A release archive is ours, but a tarball is still the
            # wrong place to trust member paths.
            for name in (BIN_NAME, *LIB_NAMES):
                try:
                    member = tar.getmember(name)
                except KeyError:
                    sys.exit(f"{name} missing from {archive}")
                if member.issym() or member.islnk():
                    sys.exit(f"unexpected link in release archive: {member.name}")
                extracted = tar.extractfile(member)
                if extracted is None:
                    sys.exit(f"{name} missing from {archive}")
                with open(BIN_DIR / name, "wb") as out:
                    shutil.copyfileobj(extracted, out)

    dest = _binary()
    dest.chmod(dest.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
    return dest


def _cached_files() -> tuple[Path, ...]:
    return tuple(BIN_DIR / name for name in (BIN_NAME, *LIB_NAMES))


def main() -> None:
    binary = _binary()
    # The whole set has to be present, not just the binary. A cache holding
    # mavor without its shared objects execs fine and then dies in the dynamic
    # loader, which is a far worse message than re-downloading.
    if not all(f.exists() for f in _cached_files()):
        binary = _download()

    try:
        sys.exit(subprocess.run([str(binary), *sys.argv[1:]]).returncode)
    except FileNotFoundError:
        sys.exit(
            f"mavor is missing from {BIN_DIR}; delete that directory and run "
            "mavor again to fetch the release archive."
        )
    except KeyboardInterrupt:
        sys.exit(130)
