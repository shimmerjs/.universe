"""Fixture tests for the cody config.toml updater. See cody-config-check.nix
(leg 8) for why. Reads OWNED_CONFIG, the updater script, from the
environment; exits non-zero with every failing case listed.
"""

import json
import os
import subprocess
import sys
import tempfile
import tomllib
from pathlib import Path

SCRIPT = os.environ["OWNED_CONFIG"]
UNIVERSE = "/Users/u/.universe"
MONO = "/Users/u/mono"
SCALARS = {
    "model": "gpt-test",
    "approval_policy": "never",
    "default_permissions": ":danger-full-access",
}
TRUSTED = {"trust_level": "trusted"}
UNTRUSTED = {"trust_level": "untrusted"}

SPEC = Path(tempfile.mkdtemp()) / "spec.json"
SPEC.write_text(json.dumps({"scalars": SCALARS, "trusted": [UNIVERSE, MONO]}))


def run(cfg, *flags):
    return subprocess.run(
        [sys.executable, SCRIPT, str(SPEC), str(cfg), *flags],
        capture_output=True,
        text=True,
    )


def fixture(text):
    cfg = Path(tempfile.mkdtemp()) / "config.toml"
    cfg.write_text(text)
    return cfg


def snapshot(cfg):
    """Everything a write would change: dir listing, bytes, inode, mtime."""
    st = cfg.stat()
    return sorted(os.listdir(cfg.parent)), cfg.read_bytes(), st.st_ino, st.st_mtime_ns


def apply(cfg):
    """Run the updater, then again to prove it converged."""
    r = run(cfg)
    assert r.returncode == 0, f"exit {r.returncode}: {r.stderr.strip()}"
    before = snapshot(cfg)
    r = run(cfg)
    assert r.returncode == 0 and snapshot(cfg) == before, "second run rewrote the file"
    assert before[0] == ["config.toml"], f"stray files left behind: {before[0]}"
    out = cfg.read_text()
    data = tomllib.loads(out)
    for key, value in SCALARS.items():
        assert data.get(key) == value, f"{key} = {data.get(key)!r}, want {value!r}\n{out}"
    return out, data


def missing_file():
    cfg = Path(tempfile.mkdtemp()) / "codex" / "config.toml"
    r = run(cfg, "--dry-run")
    assert r.returncode == 0, r.stderr.strip()
    assert not cfg.parent.exists(), "dry run created the directory"
    _, data = apply(cfg)
    assert data["projects"] == {UNIVERSE: TRUSTED, MONO: TRUSTED}, data["projects"]


def quoted_root_keys():
    out, data = apply(fixture("""\
# hand-written
"model" = "gpt-old" # pinned by hand
'approval_policy' = "on-request"

[tui]
theme = "dark" # keep
"""))
    assert out.startswith("# hand-written\n"), out
    assert "# pinned by hand" in out, out
    assert 'theme = "dark" # keep\n' in out, out
    assert data["tui"] == {"theme": "dark"}, data["tui"]


INSTRUCTIONS = '''\
developer_instructions = """
model = "not a key"
[projects."/Users/u/.universe"]
trust_level = "untrusted"
"""
'''


def multiline_string():
    text = 'model = "gpt-old"\n' + INSTRUCTIONS
    out, data = apply(fixture(text))
    assert INSTRUCTIONS in out, out
    assert data["developer_instructions"] == tomllib.loads(text)["developer_instructions"]
    assert data["projects"] == {UNIVERSE: TRUSTED, MONO: TRUSTED}, data["projects"]


def alternate_project_tables():
    out, data = apply(fixture("""\
[projects.'/Users/u/.universe']
trust_level = "untrusted"

[projects."/Users/u/mono"]
# hand-added
"""))
    assert out.count(UNIVERSE) == 1, out
    assert "[projects.'/Users/u/.universe']\n" in out, out
    assert "# hand-added" in out, out
    assert data["projects"] == {UNIVERSE: UNTRUSTED, MONO: TRUSTED}, data["projects"]


def out_of_order_project_tables():
    out, data = apply(fixture("""\
[projects."/Users/u/other"]
trust_level = "trusted"

[tui]
theme = "dark"

[projects."/Users/u/.universe"]
trust_level = "untrusted"
"""))
    assert out.count(UNIVERSE) == 1, out
    assert data["tui"] == {"theme": "dark"}, data["tui"]
    assert data["projects"] == {
        "/Users/u/other": TRUSTED,
        UNIVERSE: UNTRUSTED,
        MONO: TRUSTED,
    }, data["projects"]


def inline_projects():
    out, data = apply(fixture("""\
projects = { "/Users/u/.universe" = { trust_level = "untrusted" } }
"""))
    assert "[projects" not in out, out
    assert data["projects"] == {UNIVERSE: UNTRUSTED, MONO: TRUSTED}, data["projects"]


def dotted_projects():
    _, data = apply(fixture('''\
projects."/Users/u/.universe".trust_level = "untrusted"
projects."/Users/u/mono".note = "keep"
'''))
    assert data["projects"] == {
        UNIVERSE: UNTRUSTED,
        MONO: {"note": "keep", "trust_level": "trusted"},
    }, data["projects"]


def converged_file_is_not_rewritten():
    cfg = fixture("""\
model = "gpt-test"
approval_policy = "never"
default_permissions = ":danger-full-access"

[projects."/Users/u/.universe"]
trust_level = "trusted"

[projects."/Users/u/mono"]
trust_level = "untrusted"
""")
    before = snapshot(cfg)
    r = run(cfg)
    assert r.returncode == 0, r.stderr.strip()
    assert snapshot(cfg) == before, "rewrote a converged file"


def dry_run_writes_nothing():
    cfg = fixture('model = "gpt-old"\n')
    before = snapshot(cfg)
    r = run(cfg, "--dry-run")
    assert r.returncode == 0, r.stderr.strip()
    assert "would update" in r.stdout, r.stdout
    assert snapshot(cfg) == before, "dry run touched the directory"


MALFORMED = {
    "syntax error": 'model = "gpt-old"\n[tui\n',
    "duplicate key": 'model = "a"\nmodel = "b"\n',
    "projects not a table": 'projects = "nope"\n',
    "project entry not a table": '[projects]\n"/Users/u/.universe" = "trusted"\n',
}


def malformed_input_is_left_alone():
    for label, text in MALFORMED.items():
        cfg = fixture(text)
        before = snapshot(cfg)
        r = run(cfg)
        assert r.returncode != 0, f"{label}: accepted"
        assert "left untouched" in r.stderr, f"{label}: {r.stderr.strip()}"
        assert snapshot(cfg) == before, f"{label}: file or directory changed"


CASES = (
    missing_file,
    quoted_root_keys,
    multiline_string,
    alternate_project_tables,
    out_of_order_project_tables,
    inline_projects,
    dotted_projects,
    converged_file_is_not_rewritten,
    dry_run_writes_nothing,
    malformed_input_is_left_alone,
)

failures = []
for case in CASES:
    try:
        case()
    except AssertionError as e:
        failures.append(f"{case.__name__}: {e}")

if failures:
    for f in failures:
        print(f"FAIL: {f}", file=sys.stderr)
    sys.exit(1)

print(f"cody owned-config fixtures passed: {len(CASES)} cases")
