#!/usr/bin/env python3
"""PreToolUse(Write|Edit|MultiEdit) guard: block decorative/"ligature" Unicode in
content clod writes, ASCII only. Inspects only the NEW text (content/new_string),
so pre-existing characters in a file are never flagged - only what we add. Global
(lives in ~/.claude settings), so it fires in every project on the machine.

Exit 2 + stderr -> the tool call is denied and the message is shown back, forcing
an ASCII redo. Any unexpected input -> exit 0 (never block on a hook failure).
"""
import json
import sys

try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

tool = data.get("tool_name", "")
ti = data.get("tool_input", {}) or {}

chunks = []
if tool == "Write":
    chunks.append(ti.get("content", "") or "")
elif tool == "Edit":
    chunks.append(ti.get("new_string", "") or "")
elif tool == "MultiEdit":
    for e in (ti.get("edits") or []):
        chunks.append((e or {}).get("new_string", "") or "")
else:
    sys.exit(0)

text = "\n".join(chunks)
if not text:
    sys.exit(0)

# Decorative typography clod overuses -> the ASCII it should have written.
# Deliberately NOT "all non-ASCII": legitimate content (accents, CJK, emoji in
# data) is fine; only typographic decoration is banned.
BANNED = {
    "—": "-- or -",         # em dash
    "–": "-",               # en dash
    "…": "...",             # ellipsis
    "‘": "'", "’": "'",  # smart single quotes
    "“": '"', "”": '"',  # smart double quotes
    "→": "->", "←": "<-", "⇒": "=>", "⇐": "<=",  # arrows
    "•": "-", "▸": ">", "►": ">", "▪": "-",      # bullets
    "✓": "[x]", "✔": "[x]", "✗": "[ ]", "✘": "[ ]",  # checks
    "⚠": "!",               # warning sign
    " ": "a normal space",  # non-breaking space
}

found = [(ch, repl) for ch, repl in BANNED.items() if ch in text]
if not found:
    sys.exit(0)

out = ["clod: ASCII only in files you write. Decorative Unicode in the new content - replace and retry:"]
for ch, repl in found:
    out.append("  U+%04X %r -> use %s" % (ord(ch), ch, repl))
sys.stderr.write("\n".join(out) + "\n")
sys.exit(2)
