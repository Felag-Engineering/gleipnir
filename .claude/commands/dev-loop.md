---
description: "Pull a GitHub issue, architect a solution, review the plan adversarially, implement it, code-review in a loop, and open a PR."
argument-hint: "<issue-number> [--max-cycles N] [--no-preview]"
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

- `.dev-loop/plan.md` — the architecture plan (written in Stage 3, possibly revised in Stage 4)
- `.dev-loop/review-feedback.md` — accumulated code-review feedback across cycles (appended in Stage 6)
- `.dev-loop/refined-spec.md` — refined issue spec from brainstorming (written in Stage 1.5, if triggered)
- `.dev-loop/cycle-count` — current review cycle number (written in Stage 6)

These files are the source of truth for passing context between agents. When a stage says "pass the plan", tell the agent to read `.dev-loop/plan.md`. Do not paste plan contents into agent prompts — instruct agents to read the file.

---

## Pipeline

Execute these stages **in order**. Report progress clearly between stages.

### Stage 0 — Setup

```bash
rm -rf .dev-loop
mkdir -p .dev-loop
```

Verify `gh auth status` succeeds. If not, stop immediately and tell the user.

### Stage 1 — Fetch Issue

Run `gh issue view <issue-number> --json title,body,labels,assignees` to get the full issue details.
Parse the issue number from `$ARGUMENTS` (ignore any flags after it for now).
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

1. **Explore the codebase first.** Before asking the user anything, search for files, types, and patterns related to the issue topic. Understand what exists so your questions are informed and specific — don't ask things the code already answers.
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
7. **Save locally** — the refined spec is already at `.dev-loop/refined-spec.md` from step 6.

After the brainstorming flow completes, **re-read the updated issue** (the refined body is now the canonical issue body for all subsequent stages).

### Stage 2 — Create Branch

```
git fetch origin
git checkout -b agent/<issue-number>-<slugified-title> origin/<default-branch>
```

Keep the branch name under 60 characters. Use lowercase, hyphens only.

### Stage 3 — Architect

**Use the `architect` agent** to analyze the issue and codebase, then produce a structured implementation plan.

Pass it:
- The full issue title and body
- Instruction to read `CLAUDE.md` for project constraints, conventions, and settled architectural decisions
- Instruction to follow its system prompt output format exactly

Write the agent's plan output to `.dev-loop/plan.md`. This file is the canonical plan for all subsequent stages.

### Stage 4 — Plan Review (adversarial)

**Use the `plan-reviewer` agent** to stress-test the architecture plan.

Pass it:
- The full issue title and body
- Instruction to read the plan from `.dev-loop/plan.md`
- Instruction to read `CLAUDE.md` for project constraints — the reviewer must verify the plan doesn't violate any settled architectural decisions
- Instruction to verify claims against the actual codebase

**Decision point:**
- If verdict is **APPROVE** → proceed to Stage 5
- If verdict is **REVISE** → **use the `architect` agent** again, passing it:
  - The original issue title and body
  - Instruction to read the current plan from `.dev-loop/plan.md`
  - The reviewer's specific corrections and concerns
  - Instruction to produce a revised plan addressing the feedback

  Overwrite `.dev-loop/plan.md` with the revised plan, then proceed to Stage 5.

### Stage 5 — Implement

**Use the `developer` agent** to implement the plan.

Pass it:
- The issue title and body
- Instruction to read the plan from `.dev-loop/plan.md`
- Instruction to read `CLAUDE.md` for project constraints and code style requirements
- If this is a revision cycle: instruction to read accumulated feedback from `.dev-loop/review-feedback.md`
- Emphasis that **code must be written for humans to read** — a developer unfamiliar with the codebase should be able to understand every function without external context. Readable and maintainable code is not optional.
- Instruction to end its response with a **structured implementation summary** under a `## Implementation Summary` heading, listing: files changed, tests added/modified, and any deviations from the plan

After the developer finishes, append its implementation summary to `.dev-loop/review-feedback.md` under a `## Cycle N — Implementation` heading.

### Stage 5.5 — Test Gate

Before entering code review, verify that the implementation compiles and tests pass. This avoids burning a review cycle on broken code.

**Backend (always run):**
```bash
go build ./...
go test ./...
```

**Frontend (run if any files under `frontend/` were modified):**
```bash
cd frontend && npm run build && npx vitest run
```

Check `git diff --name-only` to determine if frontend files were touched. If only backend files changed, skip the frontend checks.

- **If all checks pass** → proceed to Stage 6.
- **If any check fails** → send the failure output back to the `developer` agent to fix, then re-run. This does NOT count as a review cycle.
- **Maximum 2 fix attempts.** If it still fails after 2 attempts, proceed to Stage 6 anyway and let the reviewer see the full picture.

### Stage 6 — Code Review

**Use the `code-reviewer` agent** to review the implementation.

Pass it:
- The issue title and body
- Instruction to read the plan from `.dev-loop/plan.md`
- Instruction to read `CLAUDE.md` for project constraints — the reviewer must verify each constraint
- Instruction to read `.dev-loop/review-feedback.md` for prior cycle context
- Instruction to run `git diff` and read modified files in full context
- Instruction to systematically verify each planned change was implemented (plan compliance check)
- Emphasis that **readability and maintainability are the highest priority** — code must be understandable by someone who has never seen the codebase

**Cycle tracking:** Before the first review, write `review_cycle=1` to `.dev-loop/cycle-count`. Increment this value each time you loop back to Stage 5 from a CHANGES_REQUESTED verdict.

**Decision point:**
- If verdict is **APPROVE** → proceed to Stage 6.5
- If verdict is **CHANGES_REQUESTED** → append the reviewer's feedback to `.dev-loop/review-feedback.md` under a `## Cycle N — Review Feedback` heading (where N is the current cycle number from `.dev-loop/cycle-count`), increment the cycle count, then go back to Stage 5
- **Maximum 3 review cycles.** If cycle 3 still gets CHANGES_REQUESTED, log a warning and proceed to Stage 6.5 anyway.

### Stage 6.5 — CLAUDE.md Freshness Check

After the implementation is approved, check whether the changes require updates to `CLAUDE.md` or `frontend/CLAUDE.md`. Review the implementation summary and `git diff --name-only` against these categories:

**Check `CLAUDE.md` if any of these changed:**
- New or renamed packages under `internal/` → update Key packages section
- New environment variables in `internal/config/config.go` → update Environment variables table
- New API routes in `internal/api/routes.go` or `main.go` → update Key API surface section
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

### Stage 8 — CI Validation

After the PR is created, wait for CI checks to complete and fix any failures.

1. **Poll for CI status** using `gh pr checks <pr-number>`. Check every 30 seconds, timeout after 10 minutes. Do not use `--watch` as it blocks without a timeout.
2. **If all checks pass** → done. Proceed to Stage 9.
3. **If any check fails:**
   a. Fetch the failed job logs: `gh run view <run-id> --log-failed`
   b. Analyze the failure output to identify which tests failed and why.
   c. **Use the `developer` agent** to fix the failing tests. Pass it:
      - The issue title and body
      - The plan (from the PR body `<details>` block or from memory if still in context)
      - The full failure log output
      - Instruction to fix the root cause (not skip/disable tests)
   d. Stage specific files, commit the fix, push, and poll CI again.
   e. **Maximum 3 fix cycles.** If cycle 3 still fails, stop and tell the user which checks are still failing so they can intervene.

### Stage 9 — Cleanup

Remove the pipeline working directory:

```bash
rm -rf .dev-loop
```

Print a final summary: issue number, PR URL, review cycles used, and whether brainstorming was triggered.

## Rules

1. **Do not skip stages.** Every stage must run even if you think you could shortcut.
2. **Use the named agents.** Do not do the architect/reviewer/developer/code-reviewer work yourself — delegate to the specialized agents.
3. **Report progress.** After each stage, print a clear status line like: `✅ Stage 3 complete: Plan produced (12 files identified)`
4. **Stop on errors.** If `gh` is not authenticated, tests catastrophically fail, or git operations fail, stop and tell the user what went wrong.
5. **Respect scope.** Only change files identified in the plan. Do not refactor unrelated code.
6. **Pass context by file, not by pasting.** Tell agents to read `.dev-loop/plan.md` and `.dev-loop/review-feedback.md` rather than copying their contents into prompts.
