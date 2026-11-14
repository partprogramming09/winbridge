"""verify_git_base must not block a repository whose history does not hold the upstream base commit (the public one-commit repo)."""

from __future__ import annotations

import importlib.util
import subprocess
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[1]


def _load():
    spec = importlib.util.spec_from_file_location("build_bundle", REPO_ROOT / "tools" / "build_bundle.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _git(cwd: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def _repo(tmp_path: Path) -> Path:
    _git(tmp_path, "init", "-q")
    _git(tmp_path, "config", "user.email", "ci@example.com")
    _git(tmp_path, "config", "user.name", "ci")
    (tmp_path / "a").write_text("a")
    _git(tmp_path, "add", "a")
    _git(tmp_path, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "one")
    return tmp_path


def test_missing_base_commit_is_a_warning_not_an_error(tmp_path, monkeypatch, capsys):
    bb = _load()
    monkeypatch.chdir(_repo(tmp_path))
    monkeypatch.setattr(bb, "REPO_ROOT", tmp_path, raising=False)
    bb.verify_git_base("0" * 40)
    assert "is not in this repository" in capsys.readouterr().err


def test_base_commit_present_but_not_an_ancestor_still_fails(tmp_path, monkeypatch):
    bb = _load()
    repo = _repo(tmp_path)
    _git(repo, "checkout", "-q", "-b", "other")
    (repo / "b").write_text("b")
    _git(repo, "add", "b")
    _git(repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "two")
    other = _git(repo, "rev-parse", "HEAD")
    _git(repo, "checkout", "-q", "-b", "side", "HEAD~1")
    monkeypatch.chdir(repo)
    monkeypatch.setattr(bb, "REPO_ROOT", repo, raising=False)
    with pytest.raises(bb.BuildError):
        bb.verify_git_base(other)


def test_base_commit_that_is_an_ancestor_passes_silently(tmp_path, monkeypatch, capsys):
    bb = _load()
    repo = _repo(tmp_path)
    base = _git(repo, "rev-parse", "HEAD")
    (repo / "b").write_text("b")
    _git(repo, "add", "b")
    _git(repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "two")
    monkeypatch.chdir(repo)
    monkeypatch.setattr(bb, "REPO_ROOT", repo, raising=False)
    bb.verify_git_base(base)
    assert capsys.readouterr().err == ""


def test_shallow_clone_still_fails(tmp_path, monkeypatch):
    bb = _load()
    repo = _repo(tmp_path)
    (repo / "b").write_text("b")
    _git(repo, "add", "b")
    _git(repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "two")
    clone = tmp_path.parent / (tmp_path.name + "-shallow")
    subprocess.run(["git", "clone", "-q", "--depth", "1", repo.as_uri(), str(clone)], check=True, capture_output=True)
    monkeypatch.chdir(clone)
    monkeypatch.setattr(bb, "REPO_ROOT", clone, raising=False)
    with pytest.raises(bb.BuildError, match="shallow"):
        bb.verify_git_base("0" * 40)
