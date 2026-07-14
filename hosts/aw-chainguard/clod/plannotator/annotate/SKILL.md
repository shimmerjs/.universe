---
name: annotate
description: Send a design doc or DEVPLAN through plannotator's browser markup UI and fold the annotations back in -- the review-gate loop for plans and designs written as files (plan mode not required). Use AFTER writing or materially revising a design/DEVPLAN doc, or when the user says annotate, mark it up, review this plan/design, or names a doc to review. NOT for trivial edits, code files, or docs the user has not asked to review.
---

# annotate

The review loop for the docs-as-files planning culture here: designs and
DEVPLANs live as markdown (docs/ trees, milestone files), never behind
plan mode, so plannotator's plan-mode hooks never see them. This skill is
the bridge: the same browser markup UI, pointed at the file.

## The invocation

Always run it in the background -- reviews are open-ended and the Bash
tool's 10-minute ceiling would kill a wandering review mid-markup:

```
plannotator annotate /abs/path/to/DEVPLAN.md --gate --json
```

run_in_background: true. The process serves the UI in the browser and
blocks until the review finishes; your turn may end while the user marks
up -- the completion notification resumes you with the result. Do not
poll, do not kill the task, do not treat silence as approval.

- --gate adds the Approve button: approval semantics, not just comments.
- --json emits one structured decision on stdout when the review ends.
- Absolute path only. One review at a time.

## The loop

1. Write or revise the doc; tell the user in one line that the review UI
   is opening, then start the background annotate.
2. On completion, read the decision JSON from the task output. Do not
   assume field names -- read what is actually there. It carries the
   approve/reject decision and the user's annotations.
3. Not approved: apply the annotations to the file -- quote each
   annotation you are acting on, edit the doc, skip nothing silently. If
   an annotation is ambiguous, ask rather than guess. Then re-run the
   annotate for the next round.
4. Approved: say so and move on. The gate decision is the user's; never
   proceed past an unapproved gate because the feedback "seems minor".

## Failure honesty

A nonzero exit or missing binary is a surfaced error, never a skipped
review: report the real output. Before the next darwin switch the
`plannotator` binary may not be on PATH yet -- say that instead of
retrying.
