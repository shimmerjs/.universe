# Synthesis discipline -- the shared spine for every verdict workflow's final stage

Deployed to `~/.claude/workflows/partials/SYNTHESIS.md` and cited by the synthesis stage of `review.js`, `audit.js`, and `design-review.js`. The synthesis agent reads it and applies it; the stage prompt keeps only the workflow-specific output shape (p0/p1/p2 vs severity groups vs go/no-go). The way `research.js` leans on the `researcher`/`skeptic` agents, a verdict stage leans on this. Cite it by its `~/.claude/...` path from the stage prompt; do not restate it in the `.js`.

## CONFIRMED-ONLY, AND SAY SO WHEN IT'S THIN
Build the verdict from CONFIRMED findings only. Anything tagged UNVERIFIED -- verifiers crashed, below the severity floor, over a cap -- is not a finding: never launder it into the confirmed list, and never silently drop it either. If the confirmed set is empty, say it looks clean and name exactly what was checked, so "nothing found" can't be read as "nothing looked at."

## ONE RECONCILED VERDICT, NOT N JSON BLOBS
Close the loop: emit a single go/no-go (or one ordered fix list), not a pile of per-agent fragments for the human to collate. One reconciled answer is the whole point of fanning out and merging back.

## NAME THE TRADE-OFF -- NO OPTION BUFFET
A recommendation that lists only upside isn't finished. State the one path you recommend and -- required -- what it makes worse or what it trades off. Do not hand back a menu of options for the human to pick from; that pushes the decision back uphill.

## CARRY THE TALLY
Surface the counts the run produced (found / fresh / confirmed / refuted / unverified / failed-verifiers). A conclusion that rests on holes -- failed verifiers, over-cap findings, partial coverage -- must say so out loud, so the reader can gate on whether the holes matter.
