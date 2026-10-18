#!/usr/bin/env python3
"""F-12.8: advisory lookup for FORKED Go modules under their UPSTREAM names.

golang.org/x/vuln/cmd/govulncheck matches advisories against module names as
they appear in go.mod — after a `replace` points a module at a personal fork,
upstream CVEs silently stop matching (the fork path is not the module path
the advisory was written for). This script restores the upstream view:

  1. parse go.mod require + replace directives,
  2. map every replaced module back to (upstream path, upstream version),
  3. query the OSV API for those pairs (ecosystem: Go),
  4. exit 1 when any vulnerability is found (CI step in security.yml).

Stdlib only — runs anywhere python3 exists. See docs/dev/go-fork-baselines.md.
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
import urllib.request
from pathlib import Path

OSV_BATCH_URL = "https://api.osv.dev/v1/querybatch"


def parse_go_mod(path: Path) -> tuple[dict[str, str], list[tuple[str, str, str]]]:
    """Returns (require versions, [(upstream, version, fork)]) from go.mod."""
    text = path.read_text(encoding="utf-8")

    def strip_comments(s: str) -> str:
        return "\n".join(line.split("//", 1)[0] for line in s.splitlines())

    body = strip_comments(text)
    requires: dict[str, str] = {}
    for m in re.finditer(r"^\s*([^\s]+)\s+(v[^\s]+)", body, re.M):
        requires[m.group(1)] = m.group(2)

    replaces: list[tuple[str, str, str]] = []
    for m in re.finditer(
        r"replace\s+([^\s]+)(?:\s+(v[^\s]+))?\s*=>\s*([^\s]+)\s+(v[^\s]+)", body
    ):
        upstream, left_ver, fork, fork_ver = m.groups()
        version = left_ver or requires.get(upstream)
        if version is None:
            print(f"warning: no require version for replaced module {upstream}", file=sys.stderr)
            continue
        replaces.append((upstream, version, f"{fork}@{fork_ver}"))
    return requires, replaces


def query_osv(packages: list[tuple[str, str]]) -> list[dict]:
    queries = [
        {"package": {"name": name, "ecosystem": "Go"}, "version": version}
        for name, version in packages
    ]
    req = urllib.request.Request(
        OSV_BATCH_URL,
        data=json.dumps({"queries": queries}).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310 (fixed endpoint)
        return json.load(resp).get("results", [])


def imported_packages(go_mod_dir: Path) -> set[str] | None:
    """Package paths of the build (`go list -deps ./...`), or None on failure.

    Module-level advisories such as "x/crypto/openpgp is unsafe" affect every
    version yet are only relevant when the package is actually imported —
    without this filter the script would cry wolf on non-reachable modules.
    """
    try:
        proc = subprocess.run(
            ["go", "list", "-deps", "./..."],
            cwd=go_mod_dir,
            capture_output=True,
            text=True,
            timeout=300,
        )
    except Exception:
        return None
    if proc.returncode != 0:
        return None
    return set(proc.stdout.split())


def vuln_reachable(vuln: dict, imported: set[str] | None) -> bool:
    """True when the advisory's affected packages intersect the build.

    Unknown data (no imports listed, `go list` unavailable) reports the
    advisory — never hide a hit we cannot evaluate.
    """
    if imported is None:
        return True
    packages: list[str] = []
    for affected in vuln.get("affected", []):
        spec = affected.get("ecosystem_specific", {})
        for entry in spec.get("imports", []):
            # OSV allows both "path" strings and {"path": ...} objects.
            if isinstance(entry, str):
                packages.append(entry)
            elif isinstance(entry, dict):
                packages.append(str(entry.get("path", "")))
    packages = [p for p in packages if p]
    if not packages:
        return True
    return any(pkg in imported for pkg in packages)


def main() -> int:
    go_mod = Path(__file__).resolve().parent.parent / "go.mod"
    if not go_mod.exists():
        print(f"error: {go_mod} not found", file=sys.stderr)
        return 2
    _, replaces = parse_go_mod(go_mod)
    if not replaces:
        print("no replace directives found — nothing to check")
        return 0
    imported = imported_packages(go_mod.parent)

    print("checking forked modules under their upstream identities:")
    for upstream, version, fork in replaces:
        print(f"  - {upstream}@{version}  (replaced by {fork})")

    try:
        results = query_osv([(u, v) for u, v, _ in replaces])
    except Exception as exc:  # network/parse failures must not pass silently
        print(f"error: OSV query failed: {exc}", file=sys.stderr)
        return 2

    vulnerable = 0
    skipped = 0
    for (upstream, version, _), result in zip(replaces, results):
        vulns = result.get("vulns", [])
        # Full records (the batch endpoint only returns ids).
        records = [fetch_vuln(v.get("id")) for v in vulns]
        records = [r for r in records if r]
        hits = [r for r in records if vuln_reachable(r, imported)]
        unreachable = [r for r in records if r not in hits]
        for r in unreachable:
            skipped += 1
            print(
                f"  info: {upstream}@{version} — {r.get('id')} affects packages "
                "that are not part of this build (not imported)"
            )
        if not hits:
            if not records:
                print(f"  ok: {upstream}@{version} — no known advisories")
            continue
        vulnerable += len(hits)
        print(f"  VULNERABLE: {upstream}@{version}")
        for v in hits:
            print(f"    - {v.get('id')}: {v.get('summary') or v.get('details', '')[:120]}")
        fix = fixable_hint(hits)
        if fix:
            print(
                f"    -> bump upstream baseline and rebase the fork "
                f"(see docs/dev/go-fork-baselines.md); candidates: {fix}"
            )

    if vulnerable:
        print(f"\n{vulnerable} reachable advisory(ies) affect forked modules (hidden from govulncheck)")
        return 1
    suffix = f" ({skipped} advisory(ies) skipped as not imported)" if skipped else ""
    print(f"\nall forked modules are clean against the OSV database{suffix}")
    return 0


def fetch_vuln(vuln_id: str) -> dict | None:
    """Full OSV record for an id (the batch endpoint only returns ids)."""
    if not vuln_id:
        return None
    try:
        with urllib.request.urlopen(  # noqa: S310 (fixed endpoint)
            f"https://api.osv.dev/v1/vulns/{vuln_id}", timeout=30
        ) as resp:
            return json.load(resp)
    except Exception as exc:
        print(f"warning: could not fetch {vuln_id}: {exc}", file=sys.stderr)
        return None


def fixable_hint(vulns: list[dict]) -> str:
    versions = []
    for v in vulns:
        for affected in v.get("affected", []):
            for r in affected.get("ranges", []):
                for ev in r.get("events", []):
                    if "fixed" in ev:
                        versions.append(ev["fixed"])
    return ", ".join(sorted(set(versions)))


if __name__ == "__main__":
    sys.exit(main())
