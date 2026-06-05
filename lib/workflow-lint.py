#!/usr/bin/env python3
"""Lint clod workflow scripts.

Two checks per *.js in $workflowsDir:
  1. JS syntax — the script body is wrapped as the Workflow engine runs it
     (an async function, so top-level await/return are legal) and parsed with
     `node --check`.
  2. agentType wiring — every `agentType:` passed to agent() must resolve to a
     string in $validAgents (built-ins + agents wired via
     programs.claude-code.agents). Literals and single-`const` aliases are
     resolved; `undefined`/`null` are skipped; anything unresolvable fails loud.

Inputs via env: $workflowsDir (a directory), $validAgents (space-separated).
Exits non-zero with a report if anything fails.
"""
import os
import re
import sys
import glob
import subprocess
import tempfile

wf_dir = os.environ["workflowsDir"]
valid = set(os.environ["validAgents"].split())

errors = []
summary = []

for path in sorted(glob.glob(os.path.join(wf_dir, "*.js"))):
    name = os.path.basename(path)
    src = open(path, encoding="utf-8").read()

    # 1) Syntax: mirror the engine — strip the leading `export` so `const meta`
    # is legal inside a function, then wrap the body in an async function so
    # top-level await/return parse. node --check only validates grammar, so the
    # injected globals being undeclared is fine.
    body = re.sub(r"^\s*export\s+const\b", "const", src, flags=re.M)
    wrapped = (
        "async function __wf(args, budget, log, phase, agent, parallel, pipeline, workflow) {\n"
        + body
        + "\n}\n"
    )
    with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as tf:
        tf.write(wrapped)
        tmp = tf.name
    res = subprocess.run(["node", "--check", tmp], capture_output=True, text=True)
    os.unlink(tmp)
    if res.returncode != 0:
        errors.append(f"{name}: SYNTAX ERROR\n{res.stderr.strip()}")
        continue  # no point checking agentType in an unparseable file

    # 2) agentType refs must resolve to a wired/built-in agent.
    used = set()
    for m in re.finditer(r"agentType\s*:\s*([^\s,}]+)", src):
        raw = m.group(1).strip()
        if raw[:1] in "'\"":
            ref = raw.strip("'\"")
        elif raw in ("undefined", "null"):
            continue
        else:
            decl = re.search(r"\bconst\s+" + re.escape(raw) + r"\s*=\s*([^\n;]+)", src)
            if not decl:
                errors.append(f"{name}: agentType `{raw}` — can't resolve to a string literal")
                continue
            val = decl.group(1).strip()
            if val in ("undefined", "null"):
                continue
            lit = re.match(r"""['"]([^'"]+)['"]""", val)
            if not lit:
                errors.append(f"{name}: agentType `{raw}` resolves to non-literal `{val}` — can't verify")
                continue
            ref = lit.group(1)
        used.add(ref)
    for ref in sorted(used):
        if ref not in valid:
            errors.append(
                f"{name}: agentType `{ref}` is not wired "
                f"(valid: {', '.join(sorted(valid))})"
            )
    summary.append(f"{name}: agents={sorted(used) or '[generic]'}")

if errors:
    print("clod workflow lint FAILED:\n", file=sys.stderr)
    for e in errors:
        print("  - " + e.replace("\n", "\n    "), file=sys.stderr)
    sys.exit(1)

print(f"clod workflow lint OK ({len(summary)} workflow(s)):")
for s in summary:
    print("  - " + s)
