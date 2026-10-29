#!/usr/bin/env bash
# What Kubernetes buys: kill an API replica and the RabbitMQ pod in the middle of a booking and check that nothing is lost or
# done twice. Three requests for two units each of a sku that has four: exactly two are Dispatched, the third is told how many
# are left, every units is held and no more, exactly two pick slips exist, and a cancelled order gives its units back
# while its pick slip stays. Needs the cluster from `make cluster-up`.
set -euo pipefail

API="${API:-http://127.0.0.1:5239}"
CTX="${CTX:-kind-golang-warehouse}"
K="kubectl --context $CTX -n warehouse"
RUN="$(date +%s)$((RANDOM % 900 + 100))"
JSON='content-type: application/json'
say() { printf '  %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }
token_of() { python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])"; }
# every call retries a few times: for a moment after a pod dies the Service may still send a request to it
retry() { for _ in $(seq 1 20); do if out="$("$@" 2>/dev/null)"; then printf '%s' "$out"; return 0; fi; sleep 1; done; return 1; }

login() { retry curl -sf -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"admin@warehouse.local\",\"password\":\"${ADMIN_PASSWORD:-WarehouseAdmin2026}\"}" | token_of; }
register() { retry curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"$1-$RUN@example.com\",\"password\":\"Resil-Passw0rd1\"}" | token_of; }
ADM="Authorization: Bearer $(login)"
A="Authorization: Bearer $(register a)"
B="Authorization: Bearer $(register b)"
C="Authorization: Bearer $(register c)"

echo "1. a sku of four units, and two requests of two units in flight"
retry curl -sf -X POST "$API/api/skus" -H "$JSON" -H "$ADM" -d "{\"code\":\"RES-$RUN\",\"name\":\"Resilience\",\"category\":\"One\",\"bin\":\"Two\",\"receivedUnits\":4}" >/dev/null || fail "could not open the sku"
order() { retry curl -sf -X POST "$API/api/orders" -H "$JSON" -H "$1" -d "{\"skuCode\":\"RES-$RUN\",\"reference\":\"REF-$RUN-$2\",\"units\":2}" | jget "['id']"; }
IDA="$(order "$A" a)"; IDB="$(order "$B" b)"

echo "2. kill an API replica and the RabbitMQ pod while the requests are still in flight"
$K delete "$($K get pods -l app.kubernetes.io/name=api -o name | head -1)" --wait=false | sed 's/^/  /'
$K delete pod rabbitmq-0 --wait=false | sed 's/^/  /'
IDC="$(order "$C" c)" # accepted even now: the request and its outbox row are one local transaction
say "a third request was accepted while the broker was down"

echo "3. everything comes back: two are Dispatched, the third is told it is short, and nothing is oversold"
$K rollout status statefulset/rabbitmq --timeout=180s >/dev/null
$K rollout status deployment/api --timeout=180s >/dev/null
status() { body="$(retry curl -sf -H "$1" "$API/api/orders/$2")" && printf '%s' "$body" | jget "['status']" || echo unknown; }
settled() { s="$(status "$1" "$2")"; [ "$s" = Dispatched ] || [ "$s" = Rejected ]; }
for _ in $(seq 1 240); do
  if settled "$A" "$IDA" && settled "$B" "$IDB" && settled "$C" "$IDC"; then break; fi
  sleep 1
done
SOLD=0; SHORT=0; WHO=""; WHOID=""
for pair in "$A|$IDA" "$B|$IDB" "$C|$IDC"; do
  s="$(status "${pair%%|*}" "${pair##*|}")"
  case "$s" in Dispatched) SOLD=$((SOLD + 1)); WHO="${pair%%|*}"; WHOID="${pair##*|}" ;; Rejected) SHORT=$((SHORT + 1)) ;; *) fail "a request is stuck in $s" ;; esac
done
[ "$SOLD" = 2 ] && [ "$SHORT" = 1 ] || fail "expected 2 Dispatched and 1 short, got $SOLD and $SHORT"
[ "$(retry curl -sf -H "$ADM" "$API/api/skus/RES-$RUN" | jget "['onHandUnits']")" = 0 ] || fail "all four units must be held, and no more"
DOCS=0
for id in "$IDA" "$IDB" "$IDC"; do
  if curl -sf -H "$ADM" "$API/api/slips/$id" >/dev/null 2>&1; then DOCS=$((DOCS + 1)); fi
done
[ "$DOCS" = 2 ] || fail "exactly two pick slips may exist, found $DOCS"
say "2 Dispatched, 1 short, 0 units left, 2 pick slips"

echo "4. a cancelled request gives its units back and its pick slip stays"
retry curl -sf -X POST -H "$WHO" "$API/api/orders/$WHOID/cancellation" >/dev/null || fail "the cancellation failed"
for _ in $(seq 1 60); do [ "$(retry curl -sf -H "$ADM" "$API/api/skus/RES-$RUN" | jget "['onHandUnits']")" = 2 ] && break; sleep 1; done
[ "$(retry curl -sf -H "$ADM" "$API/api/skus/RES-$RUN" | jget "['onHandUnits']")" = 2 ] || fail "two units must be back"
curl -sf -H "$ADM" "$API/api/slips/$WHOID" >/dev/null || fail "the pick slip must stay"
say "2 units back, the pick slip stays"
echo "resilience passed"
