#!/usr/bin/env bash
# tech-debt-sweep.sh — automated tech-debt sweep using Claude Code headless mode.
# Runs as a cron job on a self-hosted server. Each invocation picks one area,
# fixes small issues (opens a PR), and files GitHub issues for larger ones.
#
# Usage:
#   ./scripts/tech-debt-sweep.sh
#
# Auth: Uses your Claude Code subscription (Max/Pro) by default.
#       To use an API key instead, set ANTHROPIC_API_KEY before running.
#
# Optional env vars:
#   SWEEP_MODEL        — Claude model to use (default: sonnet)
#   SWEEP_MAX_TURNS    — max agent turns per run (default: 75)
#   SWEEP_LOG_DIR      — log directory (default: ~/.local/share/gleipnir-sweep/logs)
#   SWEEP_DRY_RUN      — if "true", adds --dry-run (no writes) (default: false)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODEL="${SWEEP_MODEL:-sonnet}"
MAX_TURNS="${SWEEP_MAX_TURNS:-75}"
LOG_DIR="${SWEEP_LOG_DIR:-$HOME/.local/share/gleipnir-sweep/logs}"
DRY_RUN="${SWEEP_DRY_RUN:-false}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

mkdir -p "$LOG_DIR"

cd "$REPO_ROOT"

# Clear any API key from .env so claude uses the subscription auth
unset ANTHROPIC_API_KEY

# Clean up any leftover state from a previous crashed run
git reset --hard HEAD 2>/dev/null || true
git checkout main
git pull --ff-only

# Build CLI args as an array to avoid quoting issues
CLI_ARGS=(
  -p
  --model "$MODEL"
  --max-turns "$MAX_TURNS"
  --verbose
)

if [ "$DRY_RUN" = "true" ]; then
  CLI_ARGS+=(--allowedTools "Read,Glob,Grep,Agent")
else
  CLI_ARGS+=(--allowedTools "Bash,Read,Edit,Write,Glob,Grep,Agent")
fi

# Read system prompt from file (avoids backtick/special-char issues in $())
SYSTEM_PROMPT=$(<"$REPO_ROOT/scripts/tech-debt-sweep-prompt.md")
CLI_ARGS+=(--system-prompt "$SYSTEM_PROMPT")

# Gather context to inject: existing tech-debt PRs and issues
EXISTING_PRS=$(gh pr list --label tech-debt --json number,title --jq '.[] | "#\(.number): \(.title)"' 2>/dev/null || echo "(could not list PRs)")
EXISTING_ISSUES=$(gh issue list --label tech-debt --json number,title --jq '.[] | "#\(.number): \(.title)"' 2>/dev/null || echo "(could not list issues)")

# Build the user prompt
USER_PROMPT="Run a tech-debt sweep. Here is the current state of existing tech-debt work:

OPEN TECH-DEBT PRs:
$EXISTING_PRS

OPEN TECH-DEBT ISSUES:
$EXISTING_ISSUES

Pick one focused area, scan it, and act. Remember: ONE PR max, TWO issues max."

claude "${CLI_ARGS[@]}" "$USER_PROMPT" 2>&1 | tee "$LOG_DIR/sweep-$TIMESTAMP.log"

echo ""
echo "Sweep complete. Log: $LOG_DIR/sweep-$TIMESTAMP.log"
