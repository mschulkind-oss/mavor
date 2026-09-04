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

    arch = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine)
    if arch is None:
        sys.exit(f"mavor has no build for {machine}; releases cover amd64 and arm64.")

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
            # Extract only the binary. A release archive is ours, but a
            # tarball is still the wrong place to trust member paths.
            member = tar.getmember(BIN_NAME)
            if member.issym() or member.islnk():
                sys.exit(f"unexpected link in release archive: {member.name}")
            extracted = tar.extractfile(member)
            if extracted is None:
                sys.exit(f"{BIN_NAME} missing from {archive}")
            dest = _binary()
            with open(dest, "wb") as out:
                shutil.copyfileobj(extracted, out)

    dest.chmod(dest.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
    return dest


def main() -> None:
    binary = _binary()
    if not binary.exists():
        binary = _download()

    try:
        sys.exit(subprocess.run([str(binary), *sys.argv[1:]]).returncode)
    except FileNotFoundError:
        sys.exit(f"mavor binary missing at {binary}; reinstall the package to fetch it again.")
    except KeyboardInterrupt:
        sys.exit(130)
