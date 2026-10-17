"""Apply the nix-owned slice of codex's config.toml in place.

usage: owned-config.py SPEC CONFIG [--dry-run]

SPEC is JSON: {"scalars": {key: value}, "trusted": [path, ...]}. Preserve
unowned settings and explicit trust choices. Verify the result with an
independent parser before atomic replacement; do not rewrite unchanged files.
"""

import json
import os
import sys
import tempfile
import tomllib

import tomlkit
from tomlkit.exceptions import TOMLKitError
from tomlkit.items import InlineTable


def read(path):
    try:
        with open(path, encoding="utf-8", newline="") as f:
            return f.read()
    except FileNotFoundError:
        return ""


def update(text, scalars, trusted):
    want = tomllib.loads(text)
    doc = tomlkit.parse(text)

    want.update(scalars)
    for key, value in scalars.items():
        if doc.get(key) != value:
            doc[key] = value

    if trusted:
        want_projects = want.setdefault("projects", {})
        if not isinstance(want_projects, dict):
            raise ValueError("projects is not a table")
        if "projects" not in doc:
            doc["projects"] = tomlkit.table(is_super_table=True)
        projects = doc["projects"]
        for path in trusted:
            entry = want_projects.setdefault(path, {})
            if not isinstance(entry, dict):
                raise ValueError(f"projects.{path!r} is not a table")
            if "trust_level" in entry:
                continue
            entry["trust_level"] = "trusted"
            if path in projects:
                projects[path]["trust_level"] = "trusted"
            else:
                new = tomlkit.inline_table() if isinstance(projects, InlineTable) else tomlkit.table()
                new["trust_level"] = "trusted"
                projects[path] = new

    out = tomlkit.dumps(doc)
    if tomllib.loads(out) != want:
        raise ValueError("the edit does not parse back to the intended change")
    return out


def main():
    args = sys.argv[1:]
    dry_run = "--dry-run" in args
    spec_path, path = [a for a in args if a != "--dry-run"]
    with open(spec_path) as f:
        spec = json.load(f)

    try:
        text = read(path)
        new = update(text, spec["scalars"], spec["trusted"])
    except (ValueError, TOMLKitError) as e:
        sys.exit(f"cody: {path} left untouched: {e}")
    if new == text:
        return
    if dry_run:
        print(f"cody: would update {path}")
        return

    parent = os.path.dirname(path)
    os.makedirs(parent, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=os.path.basename(path) + ".nix-", dir=parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="") as f:
            f.write(new)
        os.replace(tmp, path)
    except BaseException:
        os.unlink(tmp)
        raise


if __name__ == "__main__":
    main()
