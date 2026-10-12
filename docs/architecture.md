---
title: Jormungandr architecture
status: draft
date: 2026-10-12
related: [https://github.com/ouroboric-dev/yggdrasil/blob/main/docs/2-engineering/specs/2026-10-11-yggdrasil-v0-spec.md]
style: reference
---

# Jormungandr architecture

The loop: build, check and fix with attempts, backoff, budgets, feedback and escalation.

## Responsibilities

- Runs a bounded loop: implement, run the check ladder, feed failures back and retry, up to N
  attempts (default 3).
- Escalates when the attempts run out (spec §7.3).
- Enforces per-loop budgets.

## Depends on

- Gungnir. Odin calls it.

## Source

The v0 engineering spec, §2.1, §4.2, §7.3. When this page and the spec differ, the spec wins.
