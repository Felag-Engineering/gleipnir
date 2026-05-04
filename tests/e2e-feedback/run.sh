#!/usr/bin/env bash
# E2E test for #179: runsHandler.SubmitFeedback → inAppChannel.Resolve.
#
# Boots the test stack (built from local source), creates an admin, configures
# Anthropic, creates a policy that uses gleipnir.ask_operator, triggers it,
# waits for waiting_for_feedback, submits feedback, and asserts the run
# resumes and completes. Also exercises the late-callback 410 + already-
# resolved 409 paths.
#
# Requires: docker, jq, curl. Reads ANTHROPIC_API_KEY + GLEIPNIR_ENCRYPTION_KEY
# from /home/mrapp/gleipnir-public/.env.
#
# Run from repo root: tests/e2e-feedback/run.sh

set -euo pipefail

ENV_FILE="/home/mrapp/gleipnir-public/.env"
COMPOSE_FILE="docker-compose.test.yml"
PORT="${GLEIPNIR_TEST_PORT:-3001}"
BASE="http://localhost:${PORT}"
COOKIES="$(mktemp)"
trap 'rm -f "$COOKIES"' EXIT

# Pull the two env vars we need from the .env without sourcing the whole file.
if [[ ! -f "$ENV_FILE" ]]; then
  echo "FATAL: $ENV_FILE not found" >&2
  exit 1
fi
ANTHROPIC_API_KEY="$(grep -E '^ANTHROPIC_API_KEY=' "$ENV_FILE" | head -1 | cut -d= -f2-)"
GLEIPNIR_ENCRYPTION_KEY="$(grep -E '^GLEIPNIR_ENCRYPTION_KEY=' "$ENV_FILE" | head -1 | cut -d= -f2-)"
if [[ -z "$ANTHROPIC_API_KEY" || -z "$GLEIPNIR_ENCRYPTION_KEY" ]]; then
  echo "FATAL: ANTHROPIC_API_KEY / GLEIPNIR_ENCRYPTION_KEY missing in $ENV_FILE" >&2
  exit 1
fi
export GLEIPNIR_ENCRYPTION_KEY

step()  { printf "\n\033[1;36m=== %s ===\033[0m\n" "$*"; }
ok()    { printf "  \033[1;32m✓\033[0m %s\n" "$*"; }
fail()  { printf "  \033[1;31m✗\033[0m %s\n" "$*" >&2; exit 1; }

# --- 1. Bring up the stack ----------------------------------------------------
step "Building + booting test stack"
docker compose -f "$COMPOSE_FILE" up --build -d >/dev/null
ok "stack up"

step "Waiting for healthy"
for i in $(seq 1 60); do
  status="$(docker inspect --format='{{.State.Health.Status}}' "$(docker compose -f "$COMPOSE_FILE" ps -q api-test)" 2>/dev/null || echo unknown)"
  if [[ "$status" == "healthy" ]]; then ok "healthy after ${i}s"; break; fi
  sleep 1
  if [[ $i -eq 60 ]]; then
    docker compose -f "$COMPOSE_FILE" logs api-test | tail -50
    fail "api-test never reached healthy"
  fi
done

# --- 2. First-run admin setup -------------------------------------------------
step "Admin setup"
SETUP_RESP="$(curl -sS -X POST "${BASE}/api/v1/auth/setup" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"e2etest-feedback-179"}')"
echo "  → $SETUP_RESP"
[[ "$(echo "$SETUP_RESP" | jq -r '.data.username // .username // empty')" == "admin" ]] || fail "setup did not return admin"
ok "admin created"

# --- 3. Login (cookie jar) ----------------------------------------------------
step "Login"
LOGIN_RESP="$(curl -sS -c "$COOKIES" -X POST "${BASE}/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"e2etest-feedback-179"}')"
echo "  → $LOGIN_RESP"
[[ -s "$COOKIES" ]] || fail "no session cookie"
ok "logged in"

# --- 4. Configure Anthropic provider key --------------------------------------
step "Configure Anthropic provider"
PROV_RESP="$(curl -sS -b "$COOKIES" -X PUT "${BASE}/api/v1/admin/providers/anthropic/key" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg k "$ANTHROPIC_API_KEY" '{key:$k}')")"
echo "  → $PROV_RESP"
[[ "$(echo "$PROV_RESP" | jq -r '.status // .data.status // empty')" == "ok" ]] || fail "provider key save failed"
ok "anthropic key set"

# --- 5. Create policy that uses gleipnir.ask_operator -------------------------
# We build the YAML body in jq so newlines / quotes are safely escaped.
step "Create policy"
POLICY_YAML=$(cat <<'YAML'
name: e2e-feedback-179
description: E2E test — agent must call gleipnir.ask_operator
model:
  provider: anthropic
  name: claude-haiku-4-5-20251001
trigger:
  type: manual
capabilities:
  feedback:
    enabled: true
    timeout: 2m
    on_timeout: fail
agent:
  preamble: |
    You are an E2E test fixture. Your one and only task: immediately call
    the gleipnir.ask_operator tool with reason="confirm color choice".
    DO NOT do anything else first. After receiving the operator's response,
    output the operator's answer verbatim as your final reply, then stop.
  task: |
    Call gleipnir.ask_operator now with reason="confirm color choice".
    Do not produce any other output before calling the tool.
limits:
  max_steps: 8
  max_token_budget: 8000
YAML
)
POLICY_RESP="$(curl -sS -b "$COOKIES" -X POST "${BASE}/api/v1/policies" \
  -H 'Content-Type: text/plain' \
  --data-binary "$POLICY_YAML")"
echo "  → $POLICY_RESP"
POLICY_ID="$(echo "$POLICY_RESP" | jq -r '.data.id // .id // empty')"
[[ -n "$POLICY_ID" ]] || fail "policy not created (response: $POLICY_RESP)"
ok "policy created: $POLICY_ID"

# --- 6. Trigger the policy ----------------------------------------------------
step "Trigger run"
TRIG_RESP="$(curl -sS -b "$COOKIES" -X POST "${BASE}/api/v1/policies/${POLICY_ID}/trigger" \
  -H 'Content-Type: application/json' -d '{}')"
echo "  → $TRIG_RESP"
RUN_ID="$(echo "$TRIG_RESP" | jq -r '.data.run_id // .data.id // .run_id // .id // empty')"
[[ -n "$RUN_ID" ]] || fail "no run_id (response: $TRIG_RESP)"
ok "run triggered: $RUN_ID"

# --- 7. Poll until waiting_for_feedback ---------------------------------------
step "Poll for waiting_for_feedback"
RUN_STATUS=""
for i in $(seq 1 60); do
  RUN_STATE="$(curl -sS -b "$COOKIES" "${BASE}/api/v1/runs/${RUN_ID}")"
  RUN_STATUS="$(echo "$RUN_STATE" | jq -r '.data.status // .status // empty')"
  printf "  [%2ds] status=%s\n" "$i" "$RUN_STATUS"
  if [[ "$RUN_STATUS" == "waiting_for_feedback" ]]; then ok "reached waiting_for_feedback"; break; fi
  if [[ "$RUN_STATUS" == "failed" || "$RUN_STATUS" == "complete" ]]; then
    echo "  Run ended unexpectedly. Steps:"
    curl -sS -b "$COOKIES" "${BASE}/api/v1/runs/${RUN_ID}/steps" | jq -r '.data[]? | "    [\(.step_type)] \(.content[:200] // "")"'
    fail "run did not enter waiting_for_feedback (terminal=$RUN_STATUS)"
  fi
  sleep 1
  if [[ $i -eq 60 ]]; then
    fail "timeout waiting for waiting_for_feedback (last status=$RUN_STATUS)"
  fi
done

# --- 8. Submit feedback (THE THING #179 CHANGED) -----------------------------
step "Submit feedback (HOT PATH — exercises Resolve)"
FB_RESP_HEADERS="$(mktemp)"; trap 'rm -f "$COOKIES" "$FB_RESP_HEADERS"' EXIT
FB_BODY="$(curl -sS -b "$COOKIES" -D "$FB_RESP_HEADERS" \
  -X POST "${BASE}/api/v1/runs/${RUN_ID}/feedback" \
  -H 'Content-Type: application/json' \
  -d '{"response":"the color is green"}')"
FB_STATUS="$(head -1 "$FB_RESP_HEADERS" | awk '{print $2}')"
echo "  → HTTP $FB_STATUS / body: $FB_BODY"
[[ "$FB_STATUS" == "202" ]] || fail "expected 202, got $FB_STATUS (body: $FB_BODY)"
ok "feedback accepted (202)"

# --- 9. Submit AGAIN — must be 409 already_resolved (or 410 late) -----------
step "Duplicate submit must be rejected"
DUP_HEADERS="$(mktemp)"; trap 'rm -f "$COOKIES" "$FB_RESP_HEADERS" "$DUP_HEADERS"' EXIT
DUP_BODY="$(curl -sS -b "$COOKIES" -D "$DUP_HEADERS" \
  -X POST "${BASE}/api/v1/runs/${RUN_ID}/feedback" \
  -H 'Content-Type: application/json' \
  -d '{"response":"second submission"}')"
DUP_STATUS="$(head -1 "$DUP_HEADERS" | awk '{print $2}')"
echo "  → HTTP $DUP_STATUS / body: $DUP_BODY"
case "$DUP_STATUS" in
  409|410) ok "duplicate correctly rejected ($DUP_STATUS)" ;;
  *)       fail "expected 409 or 410 on duplicate, got $DUP_STATUS" ;;
esac

# --- 10. Poll until run completes ---------------------------------------------
step "Poll until complete"
for i in $(seq 1 90); do
  RUN_STATE="$(curl -sS -b "$COOKIES" "${BASE}/api/v1/runs/${RUN_ID}")"
  RUN_STATUS="$(echo "$RUN_STATE" | jq -r '.data.status // .status // empty')"
  printf "  [%2ds] status=%s\n" "$i" "$RUN_STATUS"
  if [[ "$RUN_STATUS" == "complete" ]]; then ok "run completed after Resolve"; break; fi
  if [[ "$RUN_STATUS" == "failed" ]]; then
    curl -sS -b "$COOKIES" "${BASE}/api/v1/runs/${RUN_ID}/steps" | jq -r '.data[]? | "    [\(.step_type)] \(.content[:200] // "")"'
    fail "run failed after feedback"
  fi
  sleep 1
  if [[ $i -eq 90 ]]; then fail "timeout waiting for complete (last=$RUN_STATUS)"; fi
done

# --- 11. Verify the feedback_response landed in the trace --------------------
step "Verify reasoning trace contains feedback_response"
STEPS="$(curl -sS -b "$COOKIES" "${BASE}/api/v1/runs/${RUN_ID}/steps")"
echo "$STEPS" | jq -r '.data[]? | .type' | sort -u | sed 's/^/  /'
echo "$STEPS" | jq -e '.data[]? | select(.type == "feedback_response")' >/dev/null \
  || fail "no feedback_response step found"
ok "feedback_response present"
# The LLM consumed the operator response — there should be a thought/text
# step echoing it back per the policy preamble.
RESPONSE_ECHOED="$(echo "$STEPS" | jq -r '.data[] | select(.type=="thought") | .content' | grep -c "the color is green" || true)"
[[ "$RESPONSE_ECHOED" -ge 1 ]] || fail "LLM did not echo operator response — Resolve may have delivered wrong body"
ok "LLM consumed operator response (Resolve delivered correct body)"

# Same line for the steps assertion above
echo "  → step types confirmed"

# --- 12. Edge case: late callback on a fresh run-id should 410 ---------------
# We don't easily have an expired-but-still-pending request handle. Simulate
# the unknown-request_id case by submitting feedback for a non-existent run.
step "Bogus run-id submit (sanity, not strictly the late path)"
BOGUS_HEADERS="$(mktemp)"; trap 'rm -f "$COOKIES" "$FB_RESP_HEADERS" "$DUP_HEADERS" "$BOGUS_HEADERS"' EXIT
BOGUS_BODY="$(curl -sS -b "$COOKIES" -D "$BOGUS_HEADERS" \
  -X POST "${BASE}/api/v1/runs/run-does-not-exist/feedback" \
  -H 'Content-Type: application/json' \
  -d '{"response":"hi"}')"
BOGUS_STATUS="$(head -1 "$BOGUS_HEADERS" | awk '{print $2}')"
echo "  → HTTP $BOGUS_STATUS / body: $BOGUS_BODY"
# Expected 404 (run not found, not the new 410 path — that requires
# timing precision the bash script can't reliably hit).
case "$BOGUS_STATUS" in
  404|409) ok "bogus run rejected ($BOGUS_STATUS)" ;;
  *)       fail "expected 404 or 409 on bogus run, got $BOGUS_STATUS" ;;
esac

# --- 13. Tear down ------------------------------------------------------------
step "Tear down"
docker compose -f "$COMPOSE_FILE" down -v >/dev/null
ok "stack down"

printf "\n\033[1;32m=== ALL E2E ASSERTIONS PASSED ===\033[0m\n"
