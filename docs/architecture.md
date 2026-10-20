---
title: Jormungandr architecture
status: draft
date: 2026-10-12
related: [https://github.com/ouroboric-dev/yggdrasil/blob/main/docs/2-engineering/specs/2026-10-11-yggdrasil-v0-spec.md, https://github.com/ouroboric-dev/yggdrasil/blob/main/docs/2-engineering/plans/2026-10-16-yggdrasil-v0-phase-4/tasks.md]
style: reference
---

# Jormungandr architecture

The loop: build, check and fix with attempts, backoff, budgets, feedback and escalation.

## Responsibilities

- Runs a bounded loop (`Run`): an implementer works, a ladder of checks and read-only reviewers judges
  the work, the findings that are still open go back to the next attempt, and after N attempts (3 by
  default) the loop escalates (spec §4.2, §7.3).
- Keeps the findings (`Book`). A finding closes only when its source confirms the fix: a check that
  passes, or a reviewer's report that carries proof, a `file:line` and the name of a check that passed.
  The implementer's claim never closes one (spec §4.6).
- Treats a reviewer's answer that can't be used as a failure, never a pass: it asks the reviewer once
  more, and then escalates (spec §4.6).
- Decides what a failed session means (`Classify`, `Retrier`): a transient failure is retried with a
  backoff and is not a loop attempt, a used-up quota pauses the run, an infrastructure failure stops
  it, and anything else counts as a loop attempt (spec §12).
- Settles a disputed finding with a check (`ReadDisputes`, `Steps.Dispute`): the implementer's final answer may
  end with a `disputes` document naming a check of the loop and a test id. After the checks ran, a dispute
  that holds overrules the finding (`Book.Overrule`); one that does not leaves it open and the next prompt says
  why (`FeedbackWith`); one no check can settle escalates with `finding-disputed`, with both sides. A reviewer's
  repeat of an overruled claim reopens it only at another location or after the overruling check failed.
- Asks the budget before every session, so none starts over a cap (spec §7.4).
- Reopens a loop that passed when something after it refuses its work, such as the merge gate (spec
  §4.5): `Reopen` starts another attempt with the findings as they were, and `Refusal` is the text the
  next implementer prompt carries.

## How a loop runs

Each attempt has two phases. In `implement`, one fresh implementer session gets the open findings and
commits its work. In `ladder`, the checks run in order and stop at the first that fails; the reviewers
run only when every check passed, in order, and each is told what it reported before and which checks
passed. The loop passes when no blocker or major finding is open. Its state (`State`) is plain data,
saved at every boundary, so a stop for a cap or a quota, or a crash, repeats no attempt and loses no
finding.

## What it does not do

It runs no session, no command and no git, and writes no ledger line. Odin gives it the `Steps` that
do, and it decides what comes next.

## Depends on

- Gungnir. Odin calls it.

## Source

The v0 engineering spec, §2.1, §4.2, §4.6, §7.3, §7.4, §12. When this page and the spec differ, the
spec wins.
