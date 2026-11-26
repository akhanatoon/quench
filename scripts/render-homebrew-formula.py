#!/usr/bin/env python3
"""Render a Homebrew Ruby formula (Formula/quench.rb) from a release version and checksums.txt.

Usage:
    python3 scripts/render-homebrew-formula.py 0.2.0 dist/checksums.txt
    python3 scripts/render-homebrew-formula.py v0.2.0 https://github.com/akhanatoon/quench/releases/download/v0.2.0/checksums.txt -o Formula/quench.rb
"""

from __future__ import annotations

import argparse
import re
import sys
import urllib.request
from pathlib import Path

DEFAULT_REPO = "akhanatoon/quench"
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")

TARGETS = (
    ("darwin", "arm64"),
    ("darwin", "amd64"),
    ("linux", "arm64"),
    ("linux", "amd64"),
)


def normalize_version(raw: str) -> str:
    ver = raw.strip()
    if ver.startswith("v"):
        ver = ver[1:]
    if not ver or any(ch.isspace() for ch in ver):
        raise ValueError(f"invalid version string: {raw!r}")
    return ver


def load_checksums_text(source: str) -> str:
    if source.startswith(("http://", "https://")):
        req = urllib.request.Request(
            source,
            headers={"User-Agent": "quench-homebrew-renderer/1.0"},
        )
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.read().decode("utf-8")
    return Path(source).read_text(encoding="utf-8")


def parse_checksums(text: str) -> dict[str, str]:
    digests: dict[str, str] = {}
    for line_no, raw_line in enumerate(text.splitlines(), start=1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) < 2:
            raise ValueError(f"malformed checksums line {line_no}: {raw_line!r}")
        sha, filename = parts[0], parts[-1].lstrip("*")
        if not SHA256_RE.match(sha):
            raise ValueError(f"invalid sha256 on line {line_no}: {sha!r}")
        digests[Path(filename).name] = sha.lower()
    return digests


def render_formula(version: str, checksums: dict[str, str], repo: str = DEFAULT_REPO) -> str:
    tag = f"v{version}"
    missing: list[str] = []
    urls: dict[tuple[str, str], str] = {}
    shas: dict[tuple[str, str], str] = {}

    for goos, goarch in TARGETS:
        archive = f"quench_{version}_{goos}_{goarch}.tar.gz"
        sha = checksums.get(archive)
        if not sha:
            missing.append(archive)
            continue
        urls[(goos, goarch)] = f"https://github.com/{repo}/releases/download/{tag}/{archive}"
        shas[(goos, goarch)] = sha

    if missing:
        raise ValueError(
            "checksums.txt is missing required release archives: " + ", ".join(missing)
        )

    return f'''class Quench < Formula
  desc "Warm-sandbox, diff-aware code verification CLI and MCP server for AI coding agents"
  homepage "https://github.com/{repo}"
  version "{version}"
  license "MIT"

  on_macos do
    on_arm do
      url "{urls[("darwin", "arm64")]}"
      sha256 "{shas[("darwin", "arm64")]}"
    end

    on_intel do
      url "{urls[("darwin", "amd64")]}"
      sha256 "{shas[("darwin", "amd64")]}"
    end
  end

  on_linux do
    on_arm do
      url "{urls[("linux", "arm64")]}"
      sha256 "{shas[("linux", "arm64")]}"
    end

    on_intel do
      url "{urls[("linux", "amd64")]}"
      sha256 "{shas[("linux", "amd64")]}"
    end
  end

  def install
    bin.install "quench"
  end

  test do
    assert_match version.to_s, shell_output("#{{bin}}/quench --version")
  end
end
'''


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Render Formula/quench.rb for akhanatoon/homebrew-tap from a version and checksums.txt."
    )
    parser.add_argument(
        "version",
        help="Release version (e.g. 0.2.0 or v0.2.0).",
    )
    parser.add_argument(
        "checksums",
        help="Path or HTTP(S) URL to GoReleaser checksums.txt.",
    )
    parser.add_argument(
        "-o",
        "--output",
        help="Write rendered formula to this file path instead of stdout.",
    )
    parser.add_argument(
        "--repo",
        default=DEFAULT_REPO,
        help=f"GitHub owner/repo for release downloads (default: {DEFAULT_REPO}).",
    )
    args = parser.parse_args(argv)

    try:
        version = normalize_version(args.version)
        raw_checksums = load_checksums_text(args.checksums)
        digests = parse_checksums(raw_checksums)
        formula = render_formula(version, digests, repo=args.repo)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    if args.output:
        out_path = Path(args.output)
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(formula, encoding="utf-8", newline="\n")
    else:
        sys.stdout.write(formula)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
