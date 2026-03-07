# Devflow — Automated Development Team

Devflow is a Claude Code coordinator that takes a GitHub Epic, decomposes it into issues, and runs an Architect → Developer → Reviewer loop on each one until the code is merged.

## Prerequisites

- `claude` CLI installed and authenticated
- `gh` CLI installed and authenticated (`gh auth login`)
- `ANTHROPIC_API_KEY` set in your environment
- A GitHub repo with issues enabled

## Setup

Devflow lives inside this repo at `devflow/`. No installation needed. The `.devflow/` artifact cache is gitignored.

## How to Run

```bash
cd gleipnir/devflow
claude
```

Then give the coordinator an epic:

```
Work epic #42
```

or

```
Work https://github.com/owner/repo/issues/42
```

That's it. The coordinator takes over.

## What Happens

```
1. Coordinator reads the epic and decomposes it into issues
   └─ YOU: review the proposed issues, approve or request changes

2. Coordinator creates the GitHub issues

3. For each issue (in dependency order):

   a. Architect reads the codebase and writes a TechSpec
      └─ YOU: optionally review the spec (ask if you want to)

   b. If the spec has open questions, coordinator asks YOU for answers

   c. Developer implements, tests, and commits on a new branch

   d. Reviewer reads the diff and checks the implementation
      └─ If changes needed: Developer revises (up to 3 iterations)

   e. Coordinator opens a PR
      └─ YOU: review the PR, then say "merge" to proceed

4. Repeat for the next issue
```

## Your Checkpoints

You are asked to act at exactly these moments:

| Moment | What you decide |
|---|---|
| After decomposition | Approve the issue list, or ask for changes |
| When spec has open questions | Answer the questions |
| Before each merge | Approve or reject the PR |

Everything else runs autonomously. You can ask the coordinator for a status update at any time.

## Controlling the Run

**Review a spec before development starts:**
> "Show me the TechSpec for issue #43 before you start the developer"

**Skip an issue:**
> "Skip issue #44, I'll handle it manually"

**Stop after the current issue:**
> "Finish this issue then stop"

**Change max iterations:**
> "Use 5 iterations instead of 3 for this issue"

**Review artifacts directly:**
The coordinator saves every TechSpec, Implementation, and ReviewReport as JSON in `.devflow/`. You can read them at any time.

## Artifacts

```
devflow/.devflow/
  techspec-42.json           ← Architect's spec for issue #42
  implementation-42-1.json   ← Developer's result, iteration 1
  review-42-1.json           ← Reviewer's report, iteration 1
  implementation-42-2.json   ← Developer's revision (if needed)
  review-42-2.json           ← Reviewer's second pass
```

## Tips for Good Epics

The coordinator is only as good as the epic it starts from. Epics that work well:

- Describe the **goal**, not the implementation — let the Architect figure out the how
- Include any **non-obvious constraints** (performance requirements, backward compat, specific files to avoid)
- Call out **explicit out-of-scope items** — the coordinator will decompose too broadly if the epic is vague

Epics that produce poor results:

- Pure feature lists with no context
- Overly broad ("refactor the whole DB layer")
- Missing success criteria

## Troubleshooting

**`gh: command not found`** — Install the GitHub CLI: https://cli.github.com

**`gh auth` fails** — Run `gh auth login` and authenticate before starting

**Artifact file missing after a task** — The sub-agent likely hit an error. Ask the coordinator: "What happened with the architect task?" and it will show you the raw output.

**Developer keeps failing review on the same issue** — At iteration 3 the coordinator will stop and ask you. You can look at the code directly and tell the coordinator what to fix, or take over the branch manually.

**Branch already exists from a previous run** — The coordinator handles this automatically and checks out the existing branch.
