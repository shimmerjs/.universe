#!/usr/bin/env python3
"""Lint clod workflow scripts.

Checks per *.js in $workflowsDir:
  1. JS syntax -- the script body is wrapped as the Workflow engine runs it
     (an async function, so top-level await/return are legal) and parsed with
     `node --check`.
  2. agentType wiring -- every `agentType:` passed to agent() must resolve to a
     string in $validAgents (built-ins + agents wired via
     programs.claude-code.agents). Literals and single-`const` aliases are
     resolved; `undefined`/`null` are skipped; anything unresolvable fails loud.
  3. no body `meta.` references -- the engine strips the meta export binding.
  4. flag spec -- parseFlags callers need an extractable `const FLAGS` literal
     with unique short aliases.

Inputs via env: $workflowsDir (a directory), $validAgents (space-separated).
Exits non-zero with a report if anything fails.
"""
import os
import re
import sys
import glob
import json
import subprocess
import tempfile

wf_dir = os.environ["workflowsDir"]
valid = set(os.environ["validAgents"].split())

errors = []
summary = []

for path in sorted(glob.glob(os.path.join(wf_dir, "*.js"))):
    name = os.path.basename(path)
    src = open(path, encoding="utf-8").read()

    # 1) Syntax: mirror the engine -- it STRIPS the whole `export const meta`
    # declaration before running the body (the meta binding does not exist at
    # runtime), then executes the rest as an async function. Strip the same
    # way (same closing-brace-at-col-0 anchor as the extractors), then wrap so
    # top-level await/return parse. node --check only validates grammar, so
    # the injected globals being undeclared is fine; dangling references to
    # the stripped meta binding are caught statically in check 3.
    body = re.sub(r"export const meta = \{[\s\S]*?\n\}", "", src, count=1)
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
            # Resolve to every string literal in the alias value: a ternary like
            # `stock ? undefined : 'reviewer'` yields its real branch (verified
            # below), while a bare variable/computed value yields none and is
            # genuinely unverifiable -> skip rather than fail.
            lits = re.findall(r"""['"]([^'"]+)['"]""", val)
            used.update(lits)
            continue
        used.add(ref)
    for ref in sorted(used):
        if ref not in valid:
            errors.append(
                f"{name}: agentType `{ref}` is not wired "
                f"(valid: {', '.join(sorted(valid))})"
            )

    # 3) the runtime strips the `export const meta` binding before running the
    # body, so ANY body reference to the `meta` identifier throws "meta is not
    # defined" at runtime. node --check cannot see it (grammar only, and the
    # old harness even re-declared the binding the engine removes -- which is
    # exactly how the 2026-07-06 breakage sailed through). Check the stripped
    # body statically, ignoring comments.
    body_no_comments = re.sub(r"//[^\n]*", "", body)
    if re.search(r"\bmeta\b", body_no_comments):
        errors.append(
            f"{name}: body references the stripped `meta` binding -- the "
            f"engine removes the meta export before running the body; move "
            f"the data to a plain const (flags live in `const FLAGS`)"
        )

    # 4) flag spec -- when a workflow calls parseFlags, the `const FLAGS`
    # literal must be eval-extractable (the cheatsheet generator slices the
    # same way), every flag needs a short alias, and shorts must be unique
    # within the workflow. Extraction mirrors cheatsheet-gen.mjs exactly:
    # slice the pure literal (closing brace at col 0), paren-wrap, eval.
    flag_note = ""
    if "parseFlags(" in src:
        node_extract = (
            "const fs=require('fs');const s=fs.readFileSync(process.argv[1],'utf8');"
            "const m=s.match(/\\nconst FLAGS = (\\{[\\s\\S]*?\\n\\})/);"
            "if(!m){console.error('no extractable FLAGS literal (closing brace must be at col 0)');process.exit(2)}"
            "let flags;try{flags=(0,eval)('('+m[1]+')')}catch(e){console.error('FLAGS eval failed: '+e.message);process.exit(2)}"
            "process.stdout.write(JSON.stringify(flags||null))"
        )
        r = subprocess.run(["node", "-e", node_extract, path], capture_output=True, text=True)
        if r.returncode != 0:
            errors.append(f"{name}: FLAGS extraction failed: {r.stderr.strip()}")
        else:
            try:
                flags = json.loads(r.stdout or "null")
            except json.JSONDecodeError as e:
                errors.append(f"{name}: FLAGS is not JSON-serializable: {e}")
                flags = None
            if not flags:
                errors.append(f"{name}: calls parseFlags but has no FLAGS block")
            else:
                shorts = {}
                for fname, fspec in flags.items():
                    sh = (fspec or {}).get("short")
                    if not sh:
                        errors.append(f"{name}: flag `{fname}` has no short alias")
                    elif sh in shorts:
                        errors.append(f"{name}: short `{sh}` collides ({shorts[sh]} vs {fname})")
                    else:
                        shorts[sh] = fname
                flag_note = f", flags={len(flags)}"

    summary.append(f"{name}: agents={sorted(used) or '[generic]'}{flag_note}")

if errors:
    print("clod workflow lint FAILED:\n", file=sys.stderr)
    for e in errors:
        print("  - " + e.replace("\n", "\n    "), file=sys.stderr)
    sys.exit(1)

print(f"clod workflow lint OK ({len(summary)} workflow(s)):")
for s in summary:
    print("  - " + s)
