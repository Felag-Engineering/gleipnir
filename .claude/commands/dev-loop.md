---
description: "Pull a GitHub issue, architect a solution, review the plan adversarially, implement it, code-review in a loop, and open a PR."
argument-hint: "<issue-number>"
allowed-tools: Bash(*), Read, Edit, Write, Grep, Glob, Agent, AskUserQuestion
---

# Agentic Development Loop

You are a development pipeline coordinator. Execute the following pipeline for GitHub issue **#$ARGUMENTS**.

## Current repo context

!git remote get-url origin 2>/dev/null || echo "no remote"
!git branch --show-current
!git status --short

---

## Shared state

All pipeline artifacts are written to `.dev-loop/` in the repo root. This directory is created at the start and cleaned up at the end.

- `.dev-loop/issue.md` — the canonical issue body (written in Stage 1, possibly rewritten in Stage 1.5)
- `.dev-loop/context.md` — codebase exploration notes (written in Stage 1.5 if brainstorming fires, re-used by architect)
- `.dev-loop/plan.md` — the architecture plan (written in Stage 3, possibly revised in Stage 4)
- `.dev-loop/review-feedback.md` — accumulated code-review feedback across cycles (appended in Stage 6)
- `.dev-loop/refined-spec.md` — refined issue spec from brainstorming (written in Stage 1.5, if triggered)

These files are the source of truth for passing context between agents. When a stage says "pass the plan", tell the agent to read `.dev-loop/plan.md`. Do not paste plan contents into agent prompts — instruct agents to read the file.

---

## Pipeline

Execute these stages **in order**. Report progress clearly between stages.

### Stage 0 — Setup

Verify `gh auth status` succeeds. If not, stop immediately and tell the user — do not touch any state yet.

Then reset the pipeline workspace:

```bash
rm -rf .dev-loop
mkdir -p .dev-loop
```

### Stage 1 — Fetch Issue

Run `gh issue view <issue-number> --json title,body,labels,assignees` to get the full issue details.
Parse the issue number from `$ARGUMENTS` (ignore any flags after it for now).

**Write the issue title and body to `.dev-loop/issue.md`** in this format:

```
# <title>

<body>
```

All subsequent stages pass the issue to subagents by instructing them to read `.dev-loop/issue.md` — do not paste the issue body into subagent prompts.

Print the issue title and a brief summary so the user can confirm before proceeding.

### Stage 1.5 — Issue Triage

Analyze the issue body to determine if it is well-specified enough to architect from. An issue is **underspecified** if **2 or more** of the following are true:

1. **No acceptance criteria** — no bullets, checkboxes, or "should" statements describing the done-state
2. **Ambiguous scope** — uses words like "improve", "better", "rework" without specifying what concretely changes
3. **Missing trigger/input** — doesn't say what triggers the behavior (webhook, cron, user action, API call, etc.)
4. **No error/edge case consideration** — doesn't mention what happens on failure or with bad input
5. **Single sentence body** — the entire body (excluding template boilerplate) is under 50 words

**Print your triage assessment:** list which criteria matched and your verdict (`well-specified` or `underspecified`).

**If well-specified** → proceed to Stage 2.

**If underspecified** → enter the brainstorming flow below, then proceed to Stage 2.

#### Brainstorming Flow (underspecified issues only)

The goal is to refine the issue into an actionable spec through a short interactive session with the user. This is NOT a full design phase — Stages 3–4 handle architecture. This is about clarifying **what** needs to happen so the architect can focus on **how**.

1. **Explore the codebase first.** Before asking the user anything, search for files, types, and patterns related to the issue topic. Understand what exists so your questions are informed and specific — don't ask things the code already answers. **Persist your findings to `.dev-loop/context.md`** as a short bullet list of files explored, types/functions discovered, and existing patterns — the architect will re-use this instead of re-exploring.
2. **Summarize what's clear** — tell the user what you understood from the issue and codebase exploration.
3. **Ask clarifying questions one at a time** using `AskUserQuestion`. Focus on the gaps identified by the triage criteria. Prefer multiple-choice questions when possible. Ask a maximum of 5 questions — stop sooner if the spec is clear enough.
4. **Propose a refined spec** — present a rewritten issue body to the user with:
   - A clear summary of the change
   - Acceptance criteria as a checkbox list
   - Scope boundaries (what's in, what's explicitly out)
   - Error/edge cases considered
5. **Get user approval** — ask the user to confirm or adjust the spec.
6. **Update the GitHub issue:**
   ```bash
   # Write the approved spec to a temp file to avoid shell escaping issues
   gh issue edit <issue-number> --body-file .dev-loop/refined-spec.md
   ```
7. **Sync local state** — copy the refined spec over `.dev-loop/issue.md` so subsequent stages see the refined body:
   ```bash
   cp .dev-loop/refined-spec.md .dev-loop/issue.md
   ```

### Stage 2 — Create Branch

```
git fetch origin
git checkout -b agent/<issue-number>-<slugified-title> origin/<default-branch>
```

Keep the branch name under 60 characters. Use lowercase, hyphens only.

### Stage 2.5 — Scope Gate (skip architect+reviewer on trivial issues)

If **all** of the following are true, skip Stages 3 and 4 and go directly to Stage 5:

1. Triage in Stage 1.5 returned `well-specified`.
2. The issue explicitly names the file(s) to modify (≤ 2 files).
3. The issue describes a localized change (a single function, a single component, a config value) — not a cross-cutting refactor, new feature, or anything touching the API/DB schema.

On skip, write a minimal plan to `.dev-loop/plan.md`:

```
IMPLEMENTATION PLAN
===================
Issue: <title>

Summary:
  <copy the acceptance criteria from the issue>

Affected files:
  <file paths from the issue>

Note: scope-gate skip — no architect pass. Developer implements directly from the issue.
```

Print: `✅ Stage 2.5: scope gate matched, skipping architect/plan-reviewer.`

Otherwise proceed to Stage 3.

### Stage 3 — Architect

**Use the `architect` agent** to analyze the issue and codebase, then produce a structured implementation plan.

Pass it:
- Instruction to read the issue from `.dev-loop/issue.md`
- If `.dev-loop/context.md` exists (from Stage 1.5 brainstorming), instruction to read it as a starting point for codebase exploration — do not re-explore what it already covers
- Instruction to follow its system prompt output format exactly

Do **not** re-state CLAUDE.md contents or the architect's own constraint list — the agent definition already bakes those in.

Write the agent's plan output to `.dev-loop/plan.md`. This file is the canonical plan for all subsequent stages.

### Stage 4 — Plan Review (adversarial)

**Use the `plan-reviewer` agent** to stress-test the architecture plan.

Pass it:
- Instruction to read the issue from `.dev-loop/issue.md`
- Instruction to read the plan from `.dev-loop/plan.md`
- Instruction to verify claims against the actual codebase (prefer LSP over Read)

**Decision point:**
- If verdict is **APPROVE** → proceed to Stage 5
- If verdict is **REVISE** → **use the `architect` agent** again, passing it:
  - Instruction to read the issue from `.dev-loop/issue.md`
  - Instruction to read the current plan from `.dev-loop/plan.md`
  - The reviewer's specific corrections and concerns
  - Instruction to produce a revised plan addressing the feedback

  Overwrite `.dev-loop/plan.md` with the revised plan, then proceed to Stage 5.

### Stage 5 — Implement

**Use the `developer` agent** to implement the plan.

Pass it:
- Instruction to read the issue from `.dev-loop/issue.md`
- Instruction to read the plan from `.dev-loop/plan.md`
- If this is a revision cycle: instruction to read accumulated feedback from `.dev-loop/review-feedback.md`
- Emphasis that **code must be written for humans to read** — a developer unfamiliar with the codebase should be able to understand every function without external context. Readable and maintainable code is not optional.
- **Explicit prohibition:** the developer agent must NOT run `git commit`, `git push`, or `gh pr create` — committing, pushing, and PR creation are Stage 7's job. The developer only edits files and runs tests.
- **Storybook stories are mandatory for new frontend components.** If the developer creates a new React component under `frontend/src/`, it must also create a matching `<Component>.stories.tsx` file demonstrating the main states. Do not skip stories.
- Instruction to end its response with a **structured implementation summary** under a `## Implementation Summary` heading, listing: files changed, tests added/modified, stories added (if any new components), and any deviations from the plan. If no code change was needed (behavior already exists), the summary must explicitly say `No changes needed`.

**If the developer returns `No changes needed`,** stop the pipeline: print the developer's explanation to the user and exit. Do not create a branch commit or PR for an empty change.

After the developer finishes, append its implementation summary to `.dev-loop/review-feedback.md` under a `## Cycle <cycle> — Implementation` heading (using the `cycle` coordinator variable; initialize `cycle = 1` on first entry to Stage 5).

### Stage 6 — Code Review

**Use the `code-reviewer` agent** to review the implementation.

The `cycle` variable was initialized in Stage 5. Increment it only on a CHANGES_REQUESTED loop-back.

Pass it:
- Instruction to read the issue from `.dev-loop/issue.md`
- Instruction to read the plan from `.dev-loop/plan.md`
- Instruction to read `.dev-loop/review-feedback.md` for prior cycle context
- Instruction to start from `git diff` and use LSP/Read **only when a hunk needs surrounding context** — do not read every modified file in full
- Instruction to systematically verify each planned change was implemented (plan compliance check)
- Emphasis that **readability and maintainability are the highest priority** — code must be understandable by someone who has never seen the codebase

**Decision point:**
- If verdict is **APPROVE** → proceed to Stage 6.5
- If verdict is **CHANGES_REQUESTED** → append the reviewer's feedback to `.dev-loop/review-feedback.md` under a `## Cycle <cycle> — Review Feedback` heading, increment `cycle`, then go back to Stage 5
- **Maximum 2 review cycles.** If cycle 2 still gets CHANGES_REQUESTED, log a warning and proceed to Stage 6.5 anyway.

### Stage 6.5 — CLAUDE.md Freshness Check

After the implementation is approved, check whether the changes require updates to `CLAUDE.md` or `frontend/CLAUDE.md`. Review the implementation summary and `git diff --name-only` against these categories:

**Check `CLAUDE.md` if any of these changed:**
- New or renamed packages under `internal/` → update Key packages section
- New environment variables in `internal/config/config.go` → update Environment variables table
- New API routes in `internal/api/router.go` (via `BuildRouter`) or `main.go` → update Key API surface section
- New trigger types, run states, step types, or roles in `internal/model/model.go` → update Core domain concepts
- New settled architectural decisions (new ADR referenced in commit) → update Settled architectural decisions

**Check `frontend/CLAUDE.md` if any of these changed:**
- New pages in `frontend/src/pages/` → update Pages section
- New routes in router config → update Route structure
- New hooks in `frontend/src/hooks/` → update Hooks section
- New shared components → update Components section
- New query keys in `queryKeys.ts` → update Query key families
- New API endpoints consumed → update API surface section

**For each file, read the current contents and compare against what the implementation changed.** If updates are needed:
1. Make the edits to keep the documentation accurate
2. Include the updated CLAUDE.md file(s) in the PR commit
3. Note in the PR body that documentation was updated

If no updates are needed, print: `Stage 6.5: CLAUDE.md files are current — no updates needed.`

### Stage 7 — Create PR

Stage changed files using specific file paths (not `git add -A`). Use the plan and implementation summary to identify which files were modified, and verify with `git status`.

```bash
git add <file1> <file2> ...
git commit -m "feat: <issue-title> (#<issue-number>)

Implemented via agentic development pipeline.

Closes #<issue-number>"
```

**The commit hook runs `go vet`, `go build`, `go test`, and — if frontend files are staged — `npm run build` and `npx vitest run`.** This replaces the old Stage 5.5 test gate: broken code cannot be committed.

**If the commit is blocked by hook failure:**
1. Capture the failure output from the hook.
2. **Use the `developer` agent** to fix the root cause. Pass it:
   - Instruction to read the issue from `.dev-loop/issue.md`
   - Instruction to read the plan from `.dev-loop/plan.md`
   - The full hook failure output (inline — this is ephemeral, not worth a file)
   - Instruction to fix the root cause — **do not skip, disable, or relax tests** to bypass the hook
   - Reminder: do not run `git commit`, `git push`, or `gh pr create` — only fix the files
3. Re-stage and re-commit.
4. **Maximum 2 fix attempts.** If still failing after 2, stop and surface the failure to the user.

After the commit succeeds:

```bash
git push -u origin <branch-name>
```

Then create the PR:

```bash
gh pr create \
  --title "feat: <issue-title>" \
  --body "<pr-body>"
```

The PR body should include:
- `Closes #<issue-number>`
- A brief summary of what was implemented
- The architecture plan in a `<details>` block
- How many review cycles were needed
- Whether CLAUDE.md was updated
- A note that this was generated by the agentic dev loop

Print a final summary: issue number, PR URL, review cycles used, and whether brainstorming was triggered.

## Rules

1. **Do not skip stages** except where a stage explicitly allows it (Stage 2.5 scope gate, Stage 5 "no changes needed" early exit). Do not invent other shortcuts.
2. **Use the named agents.** Do not do the architect/reviewer/developer/code-reviewer work yourself — delegate to the specialized agents.
3. **Report progress.** After each stage, print a clear status line like: `✅ Stage 3 complete: Plan produced (12 files identified)`
4. **Stop on errors.** If `gh` is not authenticated, tests catastrophically fail, or git operations fail, stop and tell the user what went wrong.
5. **Respect scope.** Only change files identified in the plan. Do not refactor unrelated code.
6. **Pass context by file, not by pasting.** Tell agents to read `.dev-loop/plan.md`, `.dev-loop/issue.md`, and `.dev-loop/review-feedback.md` rather than copying their contents into prompts.
7. **Only the coordinator commits.** The developer agent must never run `git commit`, `git push`, or `gh pr create`. Those are Stage 7's exclusive responsibility — this prevents premature PR creation that drops the `Closes #<issue>` linkage or skips the commit-hook test gate.
