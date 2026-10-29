#!/usr/bin/env bash
# Drives the stack running in Kubernetes over HTTP. Every code carries a run id, so a kept Postgres volume never collides.
# Needs: curl, python3. The stack must be up: make cluster-up
set -euo pipefail

API="${API:-http://127.0.0.1:5239}"
RABBIT="${RABBIT:-http://127.0.0.1:15729}"
RUN="$(date +%s)$((RANDOM % 900 + 100))"
JSON='content-type: application/json'

say() { printf '  %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }

ADMIN_EMAIL="${ADMIN_EMAIL:-admin@warehouse.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-WarehouseAdmin2026}"
CUSTOMER_PASSWORD="Smoke-Passw0rd1"
code_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
token_of() { python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])"; }

open_sku() { # code receivedUnits -> prints nothing
  curl -sf -X POST "$API/api/skus" -H "$JSON" -H "$ADM" \
    -d "{\"code\":\"$1\",\"name\":\"Smoke Widget\",\"category\":\"Hardware\",\"bin\":\"Bin A-01\",\"receivedUnits\":$2}" >/dev/null \
    || fail "could not open sku $1"
}
book() { # code reference units -> prints order id
  curl -sf -X POST "$API/api/orders" -H "$JSON" -H "$CUS" \
    -d "{\"skuCode\":\"$1\",\"reference\":\"$2\",\"units\":$3}" | jget "['id']"
}
order_field() { curl -sf -H "$ADM" "$API/api/orders/$1" | jget "['$2']"; }
remaining() { curl -sf -H "$ADM" "$API/api/skus/$1" | jget "['onHandUnits']"; }

wait_for() { # description command... : polls up to 30s until the command succeeds
  local what="$1"; shift
  for _ in $(seq 1 60); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  fail "timed out waiting for $what"
}
status_is() { [ "$(order_field "$1" status)" = "$2" ]; }
remaining_is() { [ "$(remaining "$1")" = "$2" ]; }
terminal() { case "$(order_field "$1" status)" in Dispatched|Rejected|Cancelled) return 0 ;; *) return 1 ;; esac; }
event_types() { curl -sf -H "$ADM" "$API/api/orders/$1/events" | python3 -c "import sys,json; print(' '.join(e['type'] for e in json.load(sys.stdin)))"; }
dlq_is() { [ "$(dlq_count)" = "$1" ]; }
dlq_count() { curl -sf -u platform:platform "$RABBIT/api/queues/%2F/dispatch.work.dlq" | jget "['messages']"; }

curl -sf "$API/api/health" >/dev/null || fail "API is not healthy at $API; run: make cluster-up"

echo "0. authentication and ownership"
[ "$(code_of "$API/api/skus")" = 401 ] || fail "an anonymous request should be 401"
[ "$(code_of -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-Passw0rd\"}")" = 401 ] || fail "a wrong password should be 401"
ADM="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" | token_of)" || fail "the administrator could not sign in"
CUS="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"smoke-$RUN@example.com\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)" || fail "could not register a customer"
OTHER="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"other-$RUN@example.com\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)"
[ "$(code_of -X POST "$API/api/skus" -H "$JSON" -H "$CUS" -d '{"code":"X"}')" = 403 ] || fail "a customer must not open skus"
open_sku "A-$RUN" 5
IDA="$(book "A-$RUN" "ORD-$RUN-OWN" 1)"
[ "$(code_of -H "$OTHER" "$API/api/orders/$IDA")" = 404 ] || fail "another customer must get 404 for someone else's order"
[ "$(code_of -H "$CUS" "$API/api/orders/$IDA/events")" = 403 ] || fail "the event log is staff only"
[ "$(code_of -X POST -H "$OTHER" "$API/api/orders/$IDA/cancellation")" = 404 ] || fail "another customer must not cancel it"
say "401 anonymous, 401 bad password, 403 customer opening a sku, 404 for another customer, 403 on the event log"

echo "0b. password reset ends the old sessions (needs the dev mailbox the demo stack turns on)"
RESET_EMAIL="reset-$RUN@example.com"
OLD="$(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)"
[ "$(code_of -H "Authorization: Bearer $OLD" "$API/api/auth/me")" = 200 ] || fail "the new session should work"
[ "$(code_of -X POST "$API/api/auth/forgot-password" -H "$JSON" -d "{\"email\":\"nobody-$RUN@example.com\"}")" = 202 ] || fail "an unknown address must look the same"
curl -sf -X POST "$API/api/auth/forgot-password" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\"}" >/dev/null || fail "forgot-password failed"
LINK="$(curl -sf "$API/api/dev/mailbox?to=$RESET_EMAIL" | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['link'])")"
link_param() { python3 -c "import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)['$1'][0])" "$LINK"; }
[ "$(code_of -X POST "$API/api/auth/reset-password" -H "$JSON" -d "{\"userId\":\"$(link_param reset)\",\"token\":\"$(link_param token)\",\"newPassword\":\"Another-Passw0rd9\"}")" = 204 ] || fail "reset failed"
sleep 3 # the session version is cached for a moment
[ "$(code_of -H "Authorization: Bearer $OLD" "$API/api/auth/me")" = 401 ] || fail "the old access token should be revoked"
[ "$(code_of -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\",\"password\":\"Another-Passw0rd9\"}")" = 200 ] || fail "the new password should work"
say "reset link works once, the old access token is revoked, the new password signs in"

echo "1. input is validated at the door"
open_sku "V-$RUN" 10
for body in \
  "{\"skuCode\":\"V-$RUN\",\"reference\":\"\",\"units\":1}" \
  "{\"skuCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":0}" \
  "{\"skuCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":51}" \
  "{\"skuCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":4294967297}" \
  "{\"skuCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"units\":9223372036854775807}"; do
  code="$(code_of -X POST "$API/api/orders" -H "$JSON" -H "$CUS" -d "$body")"
  [ "$code" = 400 ] || fail "expected 400 for $body, got $code"
done
say "empty, zero, over-limit and overflowing unit counts are all 400"

echo "2. happy path"
open_sku "H-$RUN" 10
ID="$(book "H-$RUN" "ORD-$RUN-1" 3)"
wait_for "order to be Dispatched" status_is "$ID" Dispatched
[ "$(order_field "$ID" feeCents)" = 850 ] || fail "fee should be 400 + 3 x 150 cents"
[ "$(curl -sf -H "$ADM" "$API/api/slips/$ID" | jget "['status']")" = Issued ] || fail "pick slip not issued"
wait_for "remaining units to be 7" remaining_is "H-$RUN" 7
TYPES="$(event_types "$ID")"
for t in stock.reserve stock.held slip.issued; do
  case "$TYPES" in *"$t"*) ;; *) fail "Kafka log is missing $t (has: $TYPES)" ;; esac
done
say "dispatched, fee 850, remaining 7, Kafka log has the three steps"

echo "3. last unit: three orders for one unit, one commits, two are short and write no hold"
open_sku "L-$RUN" 1
TMP="$(mktemp -d)"
for n in A B C; do ( book "L-$RUN" "ORD-$RUN-$n" 1 >"$TMP/$n" ) & done
wait
IDS=""
for n in A B C; do [ -s "$TMP/$n" ] || fail "order $n was not accepted"; IDS="$IDS $(cat "$TMP/$n")"; done
rm -rf "$TMP"
for i in $IDS; do wait_for "order $i to settle" terminal "$i"; done
DISPATCHED=0; SHORT=0
for i in $IDS; do
  s="$(order_field "$i" status)"
  if [ "$s" = Dispatched ]; then DISPATCHED=$((DISPATCHED + 1)); fi
  if [ "$s" = Rejected ]; then
    case "$(order_field "$i" reason)" in Short*) SHORT=$((SHORT + 1)); case "$(event_types "$i")" in *stock.held*) fail "the short loser has a hold" ;; esac ;; esac
  fi
done
[ "$DISPATCHED" = 1 ] && [ "$SHORT" = 2 ] || fail "expected 1 dispatched and 2 short, got $DISPATCHED dispatched and $SHORT short"
[ "$(remaining "L-$RUN")" = 0 ] || fail "remaining should be 0"
say "1 dispatched, 2 short, remaining 0, no hold for the losers"

echo "4. poison: committed as rejected, dead-lettered, the units come back"
open_sku "P-$RUN" 5
BEFORE="$(dlq_count)"
IDP="$(book "P-$RUN" "ORD-POISON-$RUN" 2)"
wait_for "poison order to be Rejected" status_is "$IDP" Rejected
wait_for "units to be restored to 5" remaining_is "P-$RUN" 5
wait_for "dispatch.work.dlq to gain one message" dlq_is $((BEFORE + 1))
[ "$(curl -sf -H "$ADM" "$API/api/slips/$IDP" | jget "['status']")" = Rejected ] || fail "rejection should be committed on the slip row"
[ "$(order_field "$IDP" unitsReleasedAt)" != None ] || fail "order should record the release"
say "rejected, dead-lettered (+1 in dispatch.work.dlq), remaining back to 5"

echo "4b. dead letters can be listed by staff only, and replayed"
[ "$(code_of -H "$CUS" "$API/api/admin/dead-letters")" = 403 ] || fail "dead letters are staff only"
curl -sf -H "$ADM" "$API/api/admin/dead-letters" | python3 -c "import sys,json; q={x['queue']:x for x in json.load(sys.stdin)}; assert q['dispatch.work']['count']>=1, q" || fail "the dead-letter queue should list the poison message"
BEFORE_REPLAY="$(dlq_count)"
REPLAYED="$(curl -sf -X POST -H "$ADM" "$API/api/admin/dead-letters/dispatch.work/replay?max=100" | jget "['replayed']")" || fail "replay failed"
[ "$REPLAYED" -ge 1 ] || fail "replay should have taken at least one message"
# The poison reference still fails for the same reason, so it is retried and dead-lettered again: replay is for fixed causes.
wait_for "the replayed poison message to be dead-lettered again" dlq_is "$BEFORE_REPLAY"
say "listed, replayed $REPLAYED, dead-lettered again because the cause is still there"

echo "5. cancellation races the final mark: the cancellation wins, the issued slip stays"
open_sku "R-$RUN" 6
WON=0
for attempt in 1 2 3; do
  IDR="$(book "R-$RUN" "ORD-$RUN-R$attempt" 2)"
  # The pick slip exists before the order is marked Dispatched (the final mark is delayed in the demo cluster).
  wait_for "pick slip to exist" curl -sf -H "$ADM" "$API/api/slips/$IDR"
  if status_is "$IDR" Held; then WON=1; break; fi
  say "window missed on attempt $attempt (order already Dispatched); the cancellation still has to hold; retrying"
  curl -sf -X POST -H "$CUS" "$API/api/orders/$IDR/cancellation" >/dev/null || true
  wait_for "order to be Cancelled" status_is "$IDR" Cancelled
done
[ "$WON" = 1 ] || fail "never caught the order between slip issued and final mark"
curl -sf -X POST -H "$CUS" "$API/api/orders/$IDR/cancellation" >/dev/null || fail "cancellation was refused"
wait_for "order to be Cancelled" status_is "$IDR" Cancelled
sleep 3 # longer than the final-mark delay: the late mark must not undo the cancellation
status_is "$IDR" Cancelled || fail "the final mark overwrote the cancellation"
[ "$(curl -sf -H "$ADM" "$API/api/slips/$IDR" | jget "['status']")" = Issued ] || fail "the issued slip was rolled back"
[ "$(order_field "$IDR" slipNumber)" = None ] || fail "the late final mark should have been skipped"
wait_for "units to come back" remaining_is "R-$RUN" 6
code="$(code_of -X POST -H "$CUS" "$API/api/orders/$IDR/cancellation")"
[ "$code" = 409 ] || fail "second cancellation should be 409, got $code"
say "order Cancelled, slip still Issued, remaining 6, second cancellation 409"

echo "golang-warehouse smoke passed"
