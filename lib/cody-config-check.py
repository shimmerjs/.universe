"""Validate the rendered cody (codex) harness. See cody-config-check.nix
for what each leg pins and why. Reads CODEX_DIR, SCHEMA, MODELS from the
environment; exits non-zero with every failure listed, not just the first.
"""

import json
import os
import sys
import tomllib
from pathlib import Path

import jsonschema

CODEX_DIR = Path(os.environ["CODEX_DIR"])
SCHEMA = json.loads(Path(os.environ["SCHEMA"]).read_text())
CATALOG = json.loads(Path(os.environ["MODELS"]).read_text())

# Keys a role file may carry on top of the flattened config schema.
ROLE_ONLY_KEYS = {"name", "description", "nickname_candidates"}
ROLE_REQUIRED = ("name", "description", "developer_instructions")

failures = []


def fail(msg):
    failures.append(msg)


def models():
    """slug -> set of supported reasoning efforts, from the pinned catalog."""
    out = {}
    stack = [CATALOG]
    while stack:
        node = stack.pop()
        if isinstance(node, dict):
            slug = node.get("slug")
            if isinstance(slug, str):
                efforts = {
                    lvl.get("effort")
                    for lvl in node.get("supported_reasoning_levels") or []
                    if isinstance(lvl, dict)
                }
                out.setdefault(slug, set()).update(efforts)
            stack.extend(node.values())
        elif isinstance(node, list):
            stack.extend(node)
    return out


MODELS = models()
if not MODELS:
    fail("could not extract any model slugs from the pinned catalog")


def check_schema(label, data):
    try:
        jsonschema.validate(data, SCHEMA)
    except jsonschema.ValidationError as e:
        path = ".".join(str(p) for p in e.absolute_path) or "(root)"
        fail(f"{label}: schema violation at {path}: {e.message}")


def check_model(label, data):
    slug = data.get("model")
    effort = data.get("model_reasoning_effort")
    if slug is None:
        return
    if slug not in MODELS:
        fail(f"{label}: model {slug!r} is not in the pinned codex catalog")
        return
    if effort is not None and effort not in MODELS[slug]:
        supported = ",".join(sorted(MODELS[slug]))
        fail(
            f"{label}: model {slug!r} does not support effort {effort!r} "
            f"(supported: {supported})"
        )


# 1. config.toml must stay unmanaged.
if (CODEX_DIR / "config.toml").exists():
    fail(
        "~/.codex/config.toml is nix-managed. codex rewrites that file at "
        "runtime and follows the symlink to the store, so every persisted "
        "setting will fail with EACCES. Put settings in a profile instead."
    )

# 2 + 5. Profile layers.
profiles = sorted(CODEX_DIR.glob("*.config.toml"))
if not profiles:
    fail("no *.config.toml profile layers in the rendered .codex")
for p in profiles:
    label = f"profile {p.name}"
    try:
        data = tomllib.loads(p.read_text())
    except (tomllib.TOMLDecodeError, OSError) as e:
        fail(f"{label}: unreadable or not valid TOML: {e}")
        continue
    check_schema(label, data)
    check_model(label, data)

# 3 + 4 + 5. Agent roles.
roles_root = CODEX_DIR / "agents"
# os.walk with followlinks, not Path.rglob: the roles are deployed as a
# symlinked SUBdirectory (so ~/.codex/agents stays a real dir), and rglob
# does not descend through a directory symlink. codex's own walk does.
role_files = sorted(
    Path(d) / f
    for d, _, fs in os.walk(roles_root, followlinks=True)
    for f in fs
    if f.endswith(".toml")
)
if not role_files:
    fail("no agent role .toml files under the rendered .codex/agents")
for r in role_files:
    label = f"role {r.relative_to(CODEX_DIR)}"
    if r.is_symlink():
        fail(
            f"{label}: is a symlink. codex reads role files with O_NOFOLLOW "
            "and fails at spawn time, not at load. Link the containing "
            "directory instead."
        )
        continue
    try:
        data = tomllib.loads(r.read_text())
    except (tomllib.TOMLDecodeError, OSError) as e:
        fail(f"{label}: unreadable or not valid TOML: {e}")
        continue
    for key in ROLE_REQUIRED:
        if not str(data.get(key, "")).strip():
            fail(f"{label}: missing or blank required field {key!r}")
    name = data.get("name")
    if name is not None and name != r.stem:
        fail(
            f"{label}: name {name!r} does not match the filename stem "
            f"{r.stem!r}; the file's name wins, so the role registers under "
            "a name the filename does not predict"
        )
    check_schema(label, {k: v for k, v in data.items() if k not in ROLE_ONLY_KEYS})
    check_model(label, data)

# 6. Skills must be directories holding a real SKILL.md.
skills_root = CODEX_DIR / "skills"
if skills_root.is_dir():
    for entry in sorted(skills_root.iterdir()):
        doc = entry / "SKILL.md"
        if not entry.is_dir():
            fail(f"skill {entry.name}: not a directory; codex will not see it")
        elif not doc.is_file():
            fail(f"skill {entry.name}: no SKILL.md")
        elif doc.is_symlink():
            fail(
                f"skill {entry.name}: SKILL.md is a symlink; codex's "
                "discovery walk skips symlinked non-directories silently"
            )

# 7. Managed hook payload. Hooks ship as a `[hooks]` table in the
# com.openai.codex CFPreferences domain, which codex treats as a MANAGED
# layer -- trust-exempt, so there is no hash to verify. What must hold is
# that the payload is well formed, carries NOTHING but hooks (the layer
# outranks the user config and -c, so any stray key becomes unoverridable),
# points at commands that exist, and is not shadowed by a hooks.json.
HOOK_EVENTS = {
    "PreToolUse", "PermissionRequest", "PostToolUse", "PreCompact",
    "PostCompact", "SessionStart", "SessionEnd", "UserPromptSubmit",
    "SubagentStart", "SubagentStop", "Stop", "Interrupt",
}
# Anything but SessionEnd/Interrupt defaults to a 600s timeout, long enough
# for a synchronous hook to stall a turn for ten minutes.
LONG_DEFAULT_TIMEOUT_EVENTS = HOOK_EVENTS - {"SessionEnd", "Interrupt"}

managed_path = CODEX_DIR / "nix-managed-hooks.toml"
managed = {}
if not managed_path.is_file():
    fail("no nix-managed-hooks.toml in the rendered .codex")
else:
    managed = tomllib.loads(managed_path.read_text())
stray = sorted(set(managed) - {"hooks"})
if stray:
    fail(
        f"managed payload carries top-level {stray}; that layer outranks the "
        "user's config.toml and -c, so only [hooks] belongs there"
    )

hook_count = 0
for event, groups in (managed.get("hooks") or {}).items():
    if event not in HOOK_EVENTS:
        fail(f"managed payload: {event!r} is not a codex hook event")
        continue
    for gi, group in enumerate(groups):
        matcher = group.get("matcher")
        if matcher is not None and not matcher.strip():
            fail(f"managed payload: {event}[{gi}] has an empty matcher")
        for hi, handler in enumerate(group.get("hooks", [])):
            where = f"managed payload: {event}[{gi}][{hi}]"
            hook_count += 1
            if handler.get("type") != "command":
                fail(f"{where} is not a command handler")
                continue
            cmd = handler.get("command", "")
            if not Path(cmd).is_file() or not os.access(cmd, os.X_OK):
                fail(f"{where} command is not executable: {cmd}")
            if event in LONG_DEFAULT_TIMEOUT_EVENTS and handler.get("timeout") is None:
                fail(f"{where} has no explicit timeout; it would default to 600s")
if not hook_count:
    fail("managed payload declares no hooks")

if (CODEX_DIR / "hooks.json").exists():
    fail(
        "hooks.json is deployed as well as the managed payload; codex would "
        "register every handler twice and warn about it"
    )

if failures:
    for f in failures:
        print(f"FAIL: {f}", file=sys.stderr)
    sys.exit(1)

print(
    f"cody config check passed: {len(profiles)} profiles, "
    f"{len(role_files)} roles, {hook_count} managed hooks"
)
