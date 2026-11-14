#!/usr/bin/env python3
"""Build the reviewed WinBridge Windows bundle.

The bundle is the only supported installation input. Its trust anchor is the
SHA-256 printed at the end (and delivered out of band by the Mac); the internal
manifest.json protects against accidental corruption and partial copies.

Design rules enforced here:
  * one exact WinBridge wheel, built from this tree;
  * one committed, hash-locked dependency set for Windows x64 / CPython 3.12;
  * a wheelhouse built from that lock, where every final artifact is a wheel
    (six upstream dependencies publish only sdists and are built locally);
  * a generated bundle lock naming the exact SHA-256 of every built wheel;
  * no private keys, auth keys, .env files or credential material;
  * a deterministic ZIP (sorted paths, fixed timestamps).

Run on Windows x64 with CPython 3.12 and uv installed.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import uuid
import zipfile
from datetime import UTC, datetime
from pathlib import Path

import tomllib

REPO_ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP_DIR = REPO_ROOT / "bootstrap"
DEFAULT_LOCK = BOOTSTRAP_DIR / "requirements" / "windows-py312.lock.txt"
BUNDLE_LOCK_NAME = "windows-py312-bundle.lock.txt"
DEFAULT_CONSTRAINTS = BOOTSTRAP_DIR / "requirements" / "constraints.txt"
UPSTREAM_FILE = REPO_ROOT / "upstream.json"
PYPROJECT = REPO_ROOT / "pyproject.toml"
INIT_FILE = REPO_ROOT / "src" / "winremote" / "__init__.py"

PUBLIC_KEY_RE = re.compile(
    r"^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|"
    r"sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+"
    r"([A-Za-z0-9+/=]+)(\s+.*)?$"
)
PRIVATE_KEY_MARKERS = ("-----BEGIN OPENSSH PRIVATE KEY-----", "-----BEGIN RSA PRIVATE KEY-----", "PRIVATE KEY-----")
FORBIDDEN_NAMES = {".env", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "authorized_keys.bak"}
FORBIDDEN_SUFFIXES = {".pem", ".pfx", ".p12", ".key", ".ppk"}
AUTH_KEY_LEAK_RE = re.compile(r"WINREMOTE_AUTH_KEY\s*=\s*[\"']?([A-Za-z0-9._~-]{32,})")
PIN_RE = re.compile(r"^([A-Za-z0-9_.-]+)==([^\s\\;]+)")
HASH_RE = re.compile(r"--hash=sha256:([0-9a-f]{64})")


class BuildError(RuntimeError):
    pass


def run(cmd: list[str], *, cwd: Path | None = None) -> str:
    printable = " ".join(str(part) for part in cmd)
    print(f"[build] $ {printable}")
    result = subprocess.run(
        [str(part) for part in cmd],
        cwd=str(cwd or REPO_ROOT),
        text=True,
        capture_output=True,
        check=False,
    )
    if result.returncode != 0:
        raise BuildError(f"command failed ({result.returncode}): {printable}\n{result.stdout}\n{result.stderr}")
    if result.stdout.strip():
        print(result.stdout.rstrip())
    if result.stderr.strip():
        print(result.stderr.rstrip(), file=sys.stderr)
    return result.stdout


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_contract() -> tuple[str, str, str]:
    init_text = INIT_FILE.read_text(encoding="utf-8")
    version_match = re.search(r'__version__\s*=\s*"([^"]+)"', init_text)
    upstream_match = re.search(r'UPSTREAM_BASE\s*=\s*"([^"]+)"', init_text)
    profile_match = re.search(r'PROFILE_NAME\s*=\s*"([^"]+)"', init_text)
    if not (version_match and upstream_match and profile_match):
        raise BuildError("could not read the version contract from src/winremote/__init__.py")

    with PYPROJECT.open("rb") as handle:
        project = tomllib.load(handle)
    package_version = str(project["project"]["version"])
    if package_version != version_match.group(1):
        raise BuildError(f"pyproject version {package_version} != __version__ {version_match.group(1)}")
    if profile_match.group(1) != "winbridge":
        raise BuildError("PROFILE_NAME must be winbridge")
    return version_match.group(1), upstream_match.group(1), profile_match.group(1)


def read_upstream(upstream_base: str) -> dict:
    data = json.loads(UPSTREAM_FILE.read_text(encoding="utf-8"))
    if data.get("project") != "winremote-mcp":
        raise BuildError("upstream.json project must be winremote-mcp")
    if data.get("base_version") != upstream_base:
        raise BuildError("upstream.json base_version does not match UPSTREAM_BASE")
    commit = str(data.get("base_commit", ""))
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise BuildError("upstream.json base_commit must be a full git commit")
    for artifact in data.get("artifacts", {}).values():
        if not re.fullmatch(r"[0-9a-f]{64}", str(artifact.get("sha256", ""))):
            raise BuildError("upstream.json artifact hashes must be SHA-256")
    return data


def git_state() -> tuple[str | None, bool]:
    commit: str | None = None
    dirty = False
    try:
        commit = run(["git", "rev-parse", "HEAD"]).strip()
        dirty = bool(run(["git", "status", "--porcelain"]).strip())
    except (BuildError, FileNotFoundError):
        pass
    return commit, dirty


def verify_git_base(base_commit: str) -> None:
    # A repository that starts after the fork point (the published one-commit repository) does not hold the upstream
    # commit at all: say so and go on. The ancestry check only matters where the commit exists (the upstream-merge flow).
    if run(["git", "rev-parse", "--is-shallow-repository"]).strip() == "true":
        raise BuildError("this is a shallow clone: fetch the full history before building, the upstream base cannot be checked")
    try:
        run(["git", "cat-file", "-e", f"{base_commit}^{{commit}}"])
    except BuildError:
        print(
            f"[build] warning: upstream base commit {base_commit} is not in this repository; "
            "the base check is skipped (upstream.json still records it).",
            file=sys.stderr,
        )
        return
    try:
        run(["git", "merge-base", "--is-ancestor", base_commit, "HEAD"])
    except BuildError as exc:
        raise BuildError(
            f"HEAD does not contain the fixed upstream base commit {base_commit}. "
            "Rebase the fork onto v0.4.24 before building."
        ) from exc


def parse_lock(lock_path: Path) -> list[tuple[str, str]]:
    if not lock_path.exists():
        raise BuildError(f"lock file not found: {lock_path}")
    text = lock_path.read_text(encoding="utf-8")
    if "winbridge" in text.lower() or "winremote-mcp" in text.lower():
        raise BuildError("the lock must contain dependencies only, not the WinBridge project")
    if re.search(r"^(--index-url|--extra-index-url|-e\s|--editable)", text, re.MULTILINE):
        raise BuildError("the lock must be index-free and hash-pinned")
    pinned = []
    hashes_in_block = 0
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped.endswith("\\"):
            stripped = stripped[:-1].strip()
        match = PIN_RE.match(stripped)
        if match:
            if hashes_in_block == 0 and pinned:
                raise BuildError(f"requirement {pinned[-1][0]} has no hashes")
            pinned.append((match.group(1), match.group(2)))
            hashes_in_block = 0
        elif stripped.startswith("--hash=sha256:"):
            if not HASH_RE.fullmatch(stripped):
                raise BuildError(f"malformed hash line: {stripped}")
            hashes_in_block += 1
        else:
            raise BuildError(f"unexpected lock line: {stripped}")
    if pinned and hashes_in_block == 0:
        raise BuildError(f"requirement {pinned[-1][0]} has no hashes")
    if not pinned:
        raise BuildError("the lock contains no requirements")
    fastmcp = [version for name, version in pinned if name == "fastmcp"]
    if not fastmcp or not fastmcp[0].startswith("3."):
        raise BuildError("the lock must pin fastmcp 3.x")
    return pinned


def compile_lock(lock_path: Path) -> None:
    uv = shutil.which("uv")
    if not uv:
        raise BuildError("uv is required to generate the lock (install uv or commit a reviewed lock)")
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    run(
        [
            uv,
            "pip",
            "compile",
            "pyproject.toml",
            "-c",
            str(DEFAULT_CONSTRAINTS),
            "--python-platform",
            "windows",
            "--python-version",
            "3.12",
            "--generate-hashes",
            "--no-header",
            "--no-annotate",
            "-o",
            str(lock_path),
        ]
    )
    parse_lock(lock_path)


def normalize_distribution_name(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def wheel_identity(wheel: Path) -> tuple[str, str]:
    """Return the normalized (distribution, version) of a wheel filename."""
    if wheel.suffix != ".whl":
        raise BuildError(f"not a wheel: {wheel.name}")
    parts = wheel.name[: -len(".whl")].split("-")
    if len(parts) not in {5, 6}:
        raise BuildError(f"unexpected wheel filename: {wheel.name}")
    return normalize_distribution_name(parts[0]), parts[1].replace("_", "-")


def check_wheelhouse_matches_lock(lock_path: Path, wheelhouse: Path) -> None:
    """Ensure the wheel set has exactly the packages and versions reviewed.

    Wheel hashes change when a dependency is built locally from an sdist, so
    the reviewed lock cannot verify the built artifacts. It can and must still
    verify their identities; the generated bundle lock verifies their bytes.
    """
    expected = {
        (normalize_distribution_name(name), version.replace("_", "-")) for name, version in parse_lock(lock_path)
    }
    actual: dict[tuple[str, str], str] = {}
    for wheel in sorted(wheelhouse.glob("*.whl"), key=lambda item: item.name.lower()):
        if wheel.name.lower().startswith("winbridge_mcp-"):
            continue
        identity = wheel_identity(wheel)
        if identity in actual:
            raise BuildError(f"duplicate wheels for {identity[0]}=={identity[1]}: {actual[identity]} and {wheel.name}")
        actual[identity] = wheel.name
    missing = sorted(expected - actual.keys())
    extra = sorted(actual.keys() - expected)
    if missing:
        raise BuildError(f"wheelhouse is missing reviewed packages: {missing}")
    if extra:
        raise BuildError(f"wheelhouse contains packages outside the reviewed lock: {extra}")
    print(f"[build] wheelhouse matches the reviewed lock ({len(actual)} packages)")


def build_wheelhouse(lock_path: Path, wheelhouse: Path) -> None:
    """Build/download every dependency as a wheel from the reviewed lock.

    `pip wheel` is used instead of `pip download --only-binary=:all:` because
    pyautogui, pytweening, pyscreeze, pyrect, mouseinfo and pygetwindow publish
    only sdists. pip verifies the reviewed sdist hash and builds the wheel
    locally; the generated bundle lock then pins the built bytes.
    """
    wheelhouse.mkdir(parents=True, exist_ok=True)
    for old in wheelhouse.glob("*.whl"):
        old.unlink()
    run(
        [
            sys.executable,
            "-m",
            "pip",
            "wheel",
            "--wheel-dir",
            str(wheelhouse),
            "--require-hashes",
            "--no-deps",
            "--disable-pip-version-check",
            "-r",
            str(lock_path),
        ]
    )
    wheels = sorted(wheelhouse.glob("*.whl"))
    if not wheels:
        raise BuildError("no wheels were built")
    check_wheelhouse_matches_lock(lock_path, wheelhouse)


def write_bundle_lock(wheelhouse: Path, destination: Path) -> list[tuple[str, str]]:
    """Write the install lock from the exact wheels that will ship."""
    lines = [
        "# Generated by tools/build_bundle.py from windows-py312.lock.txt.",
        "# Each hash names the exact wheel in ../wheels. Do not edit this file;",
        "# rebuild the bundle instead.",
        "",
    ]
    entries: list[tuple[str, str]] = []
    for wheel in sorted(wheelhouse.glob("*.whl"), key=lambda item: item.name.lower()):
        if wheel.name.lower().startswith("winbridge_mcp-"):
            continue
        name, version = wheel_identity(wheel)
        entries.append((name, version))
        lines.append(f"{name}=={version} \\")
        lines.append(f"    --hash=sha256:{sha256_file(wheel)}")
    if not entries:
        raise BuildError("the wheelhouse has no dependency wheels")
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text("\n".join(lines).rstrip() + "\n", encoding="utf-8", newline="\n")
    parsed = parse_lock(destination)
    if {name for name, _ in parsed} != {name for name, _ in entries}:
        raise BuildError("generated bundle lock does not round-trip")
    print(f"[build] bundle lock: {destination} ({len(entries)} packages)")
    return entries


def verify_wheelhouse(
    lock_path: Path,
    wheelhouse: Path,
    *,
    app_wheel: Path | None = None,
    expected_version: str | None = None,
) -> None:
    """Install the offline wheel set into a throwaway venv and check imports."""
    if not wheelhouse.exists():
        raise BuildError(f"wheelhouse not found: {wheelhouse}")
    with tempfile.TemporaryDirectory(prefix="winbridge-wheelcheck-") as temp:
        venv_dir = Path(temp) / "venv"
        run([sys.executable, "-m", "venv", str(venv_dir)])
        venv_python = venv_dir / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
        if not venv_python.exists():
            raise BuildError("could not create the wheel verification environment")
        run(
            [
                str(venv_python),
                "-m",
                "pip",
                "install",
                "--no-index",
                "--only-binary=:all:",
                "--find-links",
                str(wheelhouse),
                "--require-hashes",
                "--disable-pip-version-check",
                "--no-warn-script-location",
                "--no-input",
                "-r",
                str(lock_path),
            ]
        )
        run([str(venv_python), "-m", "pip", "check", "--disable-pip-version-check"])
        run([str(venv_python), "-c", "import fastmcp, starlette; print('dependency imports ok')"])
        if app_wheel is not None:
            run(
                [
                    str(venv_python),
                    "-m",
                    "pip",
                    "install",
                    "--no-deps",
                    "--disable-pip-version-check",
                    "--no-warn-script-location",
                    "--no-input",
                    str(app_wheel),
                ]
            )
            script = venv_dir / ("Scripts/winbridge-mcp.exe" if os.name == "nt" else "bin/winbridge-mcp")
            if not script.exists():
                raise BuildError("the WinBridge wheel did not install its winbridge-mcp entry point")
            check = "import winremote; print(winremote.__version__)"
            installed = run([str(venv_python), "-c", check]).strip()
            if expected_version is not None and installed != expected_version:
                raise BuildError(f"the WinBridge wheel reports {installed!r}, expected {expected_version!r}")


def build_wheel(dist_dir: Path) -> Path:
    dist_dir.mkdir(parents=True, exist_ok=True)
    for old in dist_dir.glob("winbridge_mcp-*.whl"):
        old.unlink()
    uv = shutil.which("uv")
    if uv:
        run([uv, "build", "--wheel", "--out-dir", str(dist_dir)])
    else:
        run([sys.executable, "-m", "build", "--wheel", "--outdir", str(dist_dir)])
    wheels = sorted(dist_dir.glob("winbridge_mcp-*.whl"))
    if len(wheels) != 1:
        raise BuildError(f"expected exactly one winbridge_mcp wheel, found {len(wheels)}")
    return wheels[0]


def read_public_key(path: Path) -> tuple[list[str], str]:
    lines = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if any(marker in line for marker in PRIVATE_KEY_MARKERS):
            raise BuildError("refusing to bundle a private key")
        match = PUBLIC_KEY_RE.match(line)
        if not match:
            raise BuildError(f"not a supported OpenSSH public key line: {line[:40]}...")
        lines.append(line)
    if not lines:
        raise BuildError("the SSH public key file has no keys")
    blob = base64.b64decode(lines[0].split()[1], validate=True)
    fingerprint = "SHA256:" + base64.b64encode(hashlib.sha256(blob).digest()).decode("ascii").rstrip("=")
    return lines, fingerprint


def scan_for_secrets(root: Path) -> None:
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        name = path.name.lower()
        if name in FORBIDDEN_NAMES or path.suffix.lower() in FORBIDDEN_SUFFIXES:
            raise BuildError(f"forbidden credential-like file in bundle: {path.relative_to(root)}")
        if path.stat().st_size > 20 * 1024 * 1024:
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="strict")
        except (UnicodeDecodeError, OSError):
            continue
        if any(marker in text for marker in PRIVATE_KEY_MARKERS):
            raise BuildError(f"private key material found in {path.relative_to(root)}")
        if AUTH_KEY_LEAK_RE.search(text):
            raise BuildError(f"possible auth key assignment found in {path.relative_to(root)}")


def copy_bootstrap(bundle: Path, lock_path: Path | None, bundle_lock_path: Path | None) -> None:
    """Copy the installer; a Go bundle passes no locks and carries no requirements."""
    shutil.copy2(REPO_ROOT / "bootstrap" / "bootstrap.ps1", bundle / "bootstrap.ps1")
    for relative in (Path("lib"), Path("scripts"), Path("config"), Path("jobs")):
        shutil.copytree(BOOTSTRAP_DIR / relative, bundle / "bootstrap" / relative)
    for name in ("README.txt", "authorized_keys.example"):
        shutil.copy2(BOOTSTRAP_DIR / name, bundle / "bootstrap" / name)
    if lock_path is None or bundle_lock_path is None:
        return
    shutil.copytree(BOOTSTRAP_DIR / "requirements", bundle / "bootstrap" / "requirements")
    for source in (lock_path, bundle_lock_path):
        target = bundle / "bootstrap" / "requirements" / source.name
        if target.resolve() != source.resolve():
            shutil.copy2(source, target)
    for name in ("NOTICE", "LICENSE", "upstream.json"):
        shutil.copy2(REPO_ROOT / name, bundle / name)


def write_manifest(bundle: Path, metadata: dict) -> dict:
    files = []
    for path in sorted(bundle.rglob("*"), key=lambda item: item.as_posix()):
        if not path.is_file():
            continue
        relative = path.relative_to(bundle).as_posix()
        if relative in {"manifest.json", "SHA256SUMS"}:
            continue
        files.append({"path": relative, "size": path.stat().st_size, "sha256": sha256_file(path)})
    manifest = dict(metadata)
    manifest["schema"] = 1
    manifest["files"] = files
    (bundle / "manifest.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=False) + "\n", encoding="utf-8", newline="\n"
    )
    sums = []
    for path in sorted(bundle.rglob("*"), key=lambda item: item.as_posix()):
        if not path.is_file():
            continue
        relative = path.relative_to(bundle).as_posix()
        if relative == "SHA256SUMS":
            continue
        sums.append(f"{sha256_file(path)}  {relative}")
    (bundle / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8", newline="\n")
    return manifest


def zip_bundle(bundle: Path, zip_path: Path) -> None:
    zip_path.parent.mkdir(parents=True, exist_ok=True)
    if zip_path.exists():
        zip_path.unlink()
    with zipfile.ZipFile(zip_path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for path in sorted(bundle.rglob("*"), key=lambda item: item.as_posix()):
            if not path.is_file():
                continue
            relative = path.relative_to(bundle).as_posix()
            info = zipfile.ZipInfo(relative, date_time=(2020, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 0
            info.external_attr = 0o644 << 16
            archive.writestr(info, path.read_bytes())


def verify_bundle(bundle: Path) -> dict:
    manifest_path = bundle / "manifest.json"
    if not manifest_path.exists():
        raise BuildError("manifest.json is missing")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for entry in manifest.get("files", []):
        path = bundle / entry["path"]
        if not path.exists():
            raise BuildError(f"missing bundle file: {entry['path']}")
        if sha256_file(path) != entry["sha256"]:
            raise BuildError(f"hash mismatch: {entry['path']}")
    sums_path = bundle / "SHA256SUMS"
    if sums_path.exists():
        for line in sums_path.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            expected, relative = line.split("  ", 1)
            path = bundle / relative
            if not path.exists() or sha256_file(path) != expected:
                raise BuildError(f"SHA256SUMS mismatch: {relative}")
    return manifest


def assemble(args: argparse.Namespace) -> Path:
    go_exe = Path(args.go_agent).resolve() if args.go_agent else None
    if go_exe is not None and not go_exe.is_file():
        raise BuildError(f"--go-agent file not found: {go_exe}")
    version, upstream_base, _ = read_contract()
    upstream = read_upstream(upstream_base)
    verify_git_base(str(upstream["base_commit"]))
    commit, dirty = git_state()

    wheel = lock_path = bundle_lock = wheelhouse = None
    dist_dir = Path(args.out_dir).resolve()
    dist_dir.mkdir(parents=True, exist_ok=True)
    if go_exe is None:
        lock_path = Path(args.lock).resolve() if args.lock else DEFAULT_LOCK
        if args.refresh_lock:
            if lock_path != DEFAULT_LOCK.resolve():
                raise BuildError("--refresh-lock only writes the reviewed lock path")
            compile_lock(lock_path)
        parse_lock(lock_path)

        wheelhouse = Path(args.wheelhouse).resolve()
        if args.skip_wheel_download:
            check_wheelhouse_matches_lock(lock_path, wheelhouse)
        else:
            build_wheelhouse(lock_path, wheelhouse)

        bundle_lock = wheelhouse / BUNDLE_LOCK_NAME
        write_bundle_lock(wheelhouse, bundle_lock)
        verify_wheelhouse(bundle_lock, wheelhouse)

        dist_dir = Path(args.out_dir).resolve()
        wheel = build_wheel(dist_dir)
        for old in wheelhouse.glob("winbridge_mcp-*.whl"):
            old.unlink()
        seed_wheelhouse = wheelhouse / wheel.name
        shutil.copy2(wheel, seed_wheelhouse)

    key_lines: list[str] = []
    key_fingerprint = None
    if args.ssh_public_key_file:
        key_lines, key_fingerprint = read_public_key(Path(args.ssh_public_key_file).resolve())
    elif not args.allow_no_ssh_key:
        raise BuildError(
            "an SSH public key is required for a deployable bundle; "
            "use --ssh-public-key-file, or --allow-no-ssh-key for a review-only bundle"
        )

    build_root = Path(args.build_dir).resolve()
    if build_root.exists():
        shutil.rmtree(build_root)
    bundle = build_root / f"winbridge-{version}"
    bundle.mkdir(parents=True)

    copy_bootstrap(bundle, lock_path, bundle_lock)
    wheels_dir = bundle / "wheels"
    if go_exe is None:
        wheels_dir.mkdir()
        for artifact in sorted(wheelhouse.glob("*.whl")):
            shutil.copy2(artifact, wheels_dir / artifact.name)
        if not (wheels_dir / wheel.name).exists():
            raise BuildError("the WinBridge wheel is missing from the bundle wheelhouse")
    else:
        (bundle / "agent").mkdir()
        shutil.copy2(go_exe, bundle / "agent" / "winbridge-agent.exe")
    if key_lines:
        (bundle / "bootstrap" / "authorized_keys").write_text(
            "\n".join(key_lines) + "\n", encoding="utf-8", newline="\n"
        )

    scan_for_secrets(bundle)
    metadata = {
        "product": "winbridge-mcp",
        "version": version,
        "profile": "winbridge",
        "upstream_base": upstream_base,
        "upstream_commit": upstream["base_commit"],
        "platform": "windows-x86_64",
        "created_at": datetime.now(UTC).replace(microsecond=0).isoformat(),
        "build_id": uuid.uuid4().hex,
        "git_commit": commit,
        "git_dirty": dirty,
        "ssh_key_fingerprint": key_fingerprint,
    }
    if go_exe is None:
        metadata.update(
            {
                "python": "3.12",
                "lock_sha256": sha256_file(lock_path),
                "bundle_lock": BUNDLE_LOCK_NAME,
                "bundle_lock_sha256": sha256_file(bundle_lock),
                "app_wheel": {"filename": wheel.name, "sha256": sha256_file(wheel)},
            }
        )
    else:
        metadata["agent_exe"] = {"path": "agent/winbridge-agent.exe", "sha256": sha256_file(go_exe)}
    manifest = write_manifest(bundle, metadata)
    verify_bundle(bundle)

    if args.no_zip:
        print(f"[build] bundle directory: {bundle}")
    else:
        zip_path = dist_dir / f"winbridge-{version}-bundle.zip"
        zip_bundle(bundle, zip_path)
        print(f"[build] bundle: {zip_path}")
        print(f"[build] bundle SHA-256: {sha256_file(zip_path)}")
        print(f"[build] bundle size: {zip_path.stat().st_size} bytes")

    print(
        json.dumps(
            {
                "version": version,
                "upstream_base": manifest["upstream_base"],
                "build_id": manifest["build_id"],
                "files": len(manifest["files"]),
                "wheels": len(list(wheels_dir.glob("*.whl"))) if go_exe is None else 0,
                "ssh_key_fingerprint": key_fingerprint,
                "git_dirty": dirty,
            },
            indent=2,
        )
    )
    return bundle


def verify_zip(path: Path) -> None:
    with tempfile.TemporaryDirectory(prefix="winbridge-verify-") as temp:
        root = Path(temp)
        with zipfile.ZipFile(path) as archive:
            archive.extractall(root)
        manifest = verify_bundle(root)
        print(
            json.dumps(
                {
                    "ok": True,
                    "version": manifest.get("version"),
                    "files": len(manifest.get("files", [])),
                    "ssh_key_fingerprint": manifest.get("ssh_key_fingerprint"),
                },
                indent=2,
            )
        )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Build the WinBridge review/deploy bundle.")
    parser.add_argument("--ssh-public-key-file", help="Mac OpenSSH public key to inject")
    parser.add_argument(
        "--allow-no-ssh-key",
        action="store_true",
        help="allow a review-only bundle without an injected SSH public key",
    )
    parser.add_argument("--out-dir", default="dist", help="output directory for the wheel and ZIP")
    parser.add_argument("--build-dir", default="build/bundle", help="temporary bundle assembly directory")
    parser.add_argument("--wheelhouse", default="build/wheelhouse", help="offline wheel cache")
    parser.add_argument("--skip-wheel-download", action="store_true", help="reuse an existing verified wheelhouse")
    parser.add_argument("--refresh-lock", action="store_true", help="regenerate the committed hash lock")
    parser.add_argument("--lock", default=None, help="alternative lock file (verify only)")
    parser.add_argument("--no-zip", action="store_true", help="leave the bundle as a directory")
    parser.add_argument("--verify", help="verify an existing bundle ZIP")
    parser.add_argument(
        "--go-agent",
        help="winbridge-agent.exe to bundle instead of Python and wheels (can be built on any OS)",
    )
    args = parser.parse_args(argv)

    if args.verify:
        verify_zip(Path(args.verify).resolve())
        return 0

    if not args.go_agent:
        if os.name != "nt":
            raise BuildError("build the Windows bundle on Windows")
        if platform.machine().lower() not in {"amd64", "x86_64"}:
            raise BuildError("build the Windows bundle on x64 Windows")
        if sys.version_info[:2] != (3, 12):
            raise BuildError(f"build with CPython 3.12, found {sys.version.split()[0]}")

    assemble(args)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except BuildError as exc:
        print(f"[build] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
