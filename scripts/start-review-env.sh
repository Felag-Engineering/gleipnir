#!/usr/bin/env bash
# start-review-env.sh — ensures the full stack and Storybook are running locally for UI review.
# Safe to run repeatedly; skips steps that are already done.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_PORT="${GLEIPNIR_PORT:-3000}"
STORYBOOK_PORT=6006
STORYBOOK_DIR="$REPO_ROOT/frontend/storybook-static"
STORYBOOK_PID_FILE="/tmp/gleipnir-storybook.pid"

# ── helpers ──────────────────────────────────────────────────────────────────

port_open() {
  curl -sf "http://localhost:$1" >/dev/null 2>&1
}

wait_for_port() {
  local port="$1" label="$2" timeout="${3:-60}"
  echo "  Waiting for $label on :$port ..."
  local i=0
  until port_open "$port"; do
    sleep 2
    i=$((i + 2))
    if [ "$i" -ge "$timeout" ]; then
      echo "  ERROR: $label did not become ready within ${timeout}s" >&2
      exit 1
    fi
  done
  echo "  ✓ $label is up"
}

# ── 1. Full stack (Docker Compose) ───────────────────────────────────────────

echo "==> Checking full stack (port $APP_PORT)..."
if port_open "$APP_PORT"; then
  echo "  ✓ Stack already running"
else
  echo "  Starting Docker Compose stack..."
  cd "$REPO_ROOT"

  if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "  WARNING: ANTHROPIC_API_KEY is not set — the Go backend will start but agent runs will fail"
  fi

  docker compose up -d --build
  wait_for_port "$APP_PORT" "full stack" 120
fi

# ── 2. Storybook (static, served with http-server) ───────────────────────────

echo "==> Checking Storybook (port $STORYBOOK_PORT)..."
if port_open "$STORYBOOK_PORT"; then
  echo "  ✓ Storybook already running"
else
  echo "  Building Storybook static site..."
  cd "$REPO_ROOT/frontend"
  npm run build-storybook

  # Kill any stale server
  if [ -f "$STORYBOOK_PID_FILE" ]; then
    old_pid=$(cat "$STORYBOOK_PID_FILE")
    kill "$old_pid" 2>/dev/null || true
    rm -f "$STORYBOOK_PID_FILE"
  fi

  echo "  Starting static file server for Storybook..."
  # npx serve is available wherever Node is. Runs in background.
  npx --yes serve "$STORYBOOK_DIR" --listen "$STORYBOOK_PORT" --no-request-logging &
  echo $! > "$STORYBOOK_PID_FILE"

  wait_for_port "$STORYBOOK_PORT" "Storybook" 30
fi

# ── Done ─────────────────────────────────────────────────────────────────────

echo ""
echo "Review environment ready:"
echo "  App        → http://localhost:$APP_PORT"
echo "  Storybook  → http://localhost:$STORYBOOK_PORT"
echo ""
echo "To stop Storybook: kill \$(cat $STORYBOOK_PID_FILE)"
echo "To stop the stack: docker compose down"
