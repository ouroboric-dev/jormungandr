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
- Keeps the findings (`Book`). A failed check gives one finding per failure (ids `check-<name>-<12 hex>`, built by
  gleipnir): `CheckFailed` raises or reopens each and closes the open ones of that check it no longer reports;
  `CheckPassed` closes all of them. A finding closes only when its source confirms the fix: a check that
  passes, or a reviewer's report that carries proof, a `file:line` and the name of a check that passed.
  The implementer's claim never closes one (spec §4.6).
- Works on typed answers: `Steps.Implement` returns a `schema.Implement` (`ygg.implement/v1`) and
  `Steps.Review` a `schema.Review` (`ygg.review/v1`), which Odin has read and decoded. `AnswerInstructions`
  tells a session how to hand its answer back: the contract's fields, and its schema as one JSON document.
- Treats a reviewer's answer that can't be used as a failure, never a pass: an `*UnreadableError` from
  `Steps.Review`, a `blocked` report, or a report that drops a finding. It asks the reviewer once more, and
  then escalates (spec §4.6): as `reviewer-blocked`, with the reason, when the second answer was `blocked`,
  and as `reviewer-unreadable` otherwise.
- Ends the loop at once when the implementer answers `blocked`: it escalates as `implementer-blocked` with
  the reason, and no check or reviewer runs for that attempt.
- Decides what a failed session means (`Classify`, `Retrier`): a transient failure is retried with a
  backoff and is not a loop attempt, a used-up quota pauses the run, an infrastructure failure stops
  it, and anything else counts as a loop attempt (spec §12).
- Settles a disputed finding with a check (`SettleDispute`): a dispute names a check of the loop and a test id,
  and holds only when that check ran in the attempt and its typed result lists the test among the tests that
  passed (`CheckOutcome.PassedTests`) and not among those that failed. One that holds overrules the finding
  (`Book.Overrule`); one that does not leaves it open and the next prompt says why (`FeedbackWith`); one that
  names a check the loop does not have escalates as `finding-disputed`, with both sides. A reviewer's repeat
  of an overruled claim reopens it only at another location or after the overruling check failed.
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
