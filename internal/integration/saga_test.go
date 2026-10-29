package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-warehouse/internal/dispatch"
	"github.com/joshbarros/golang-warehouse/internal/orders"
	"github.com/joshbarros/golang-warehouse/internal/platform/messaging"
)

func (e *env) outbox(schema, key string) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+schema+`.outbox_messages WHERE routing_key = $1`, key).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) count(sql string, args ...any) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) onHandUnits(skuCode string) float64 {
	e.t.Helper()
	v, _ := e.get("/api/skus/"+skuCode, e.admin().Token).Body["onHandUnits"].(float64)
	return v
}

func status(e *env, s session, id string) string { return e.order(s, id).str("status") }

func feeOf(t *testing.T, units int) float64 {
	t.Helper()
	fee, err := orders.FeeCents(units)
	if err != nil {
		t.Fatal(err)
	}
	return float64(fee)
}

func TestTheWholeSagaFromOrderToDispatchedAndTheSlip(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(10)
	ana := e.register()
	created := e.enter(ana, sku, "COL-1", 2)
	id := created.str("id")
	if created.Status != 202 || created.str("status") != "Requested" || created.Body["feeCents"] != feeOf(t, 2) {
		t.Fatalf("accepted as Requested with the fee: %s", created.Raw)
	}
	if dead := e.drain(); dead != 0 {
		t.Fatalf("nothing should dead-letter: %d", dead)
	}
	o := e.order(ana, id)
	if o.str("status") != "Dispatched" || !strings.HasPrefix(o.str("slipNumber"), "PS-") || o.Body["reason"] != nil {
		t.Fatalf("sold with a slip number and no leftover reason: %s", o.Raw)
	}
	if e.onHandUnits(sku) != 8 {
		t.Fatal("2 of 10 units are held")
	}
	c := e.get("/api/slips/"+id, ana.Token)
	if c.Status != 200 || c.str("status") != "Issued" || c.str("number") != o.str("slipNumber") {
		t.Fatalf("the slip: %d %s", c.Status, c.Raw)
	}
}

func TestWhenTwoOrdersFightForTheLastUnitsOneCommitsAndTheOtherIsShortAndWritesNoHold(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(3)
	ana, bob := e.register(), e.register()
	a := e.enter(ana, sku, "COL-A", 2).str("id")
	e.drain()
	b := e.enter(bob, sku, "COL-B", 2).str("id")
	e.drain()
	if status(e, ana, a) != "Dispatched" {
		t.Fatal("the first order is sold")
	}
	o := e.order(bob, b)
	if o.str("status") != "Rejected" || o.str("reason") != "Short: 2 units requested, 1 remaining." {
		t.Fatalf("the second is short: %s", o.Raw)
	}
	if e.get("/api/slips/"+b, bob.Token).Status != 404 || e.count(`SELECT count(*) FROM inventory.unit_holds`) != 1 {
		t.Fatal("no slip and no hold for a refused order")
	}
	if e.onHandUnits(sku) != 1 {
		t.Fatal("the refused order took nothing")
	}
}

func TestPoisonIsRejectedAndTheUnitsComeBack(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(5)
	ana := e.register()
	id := e.enter(ana, sku, "COL-POISON-1", 2).str("id")
	if dead := e.drain(); dead != 1 {
		t.Fatalf("the poison slip is dead-lettered once: %d", dead)
	}
	o := e.order(ana, id)
	if o.str("status") != "Rejected" || !strings.Contains(o.str("reason"), "poison") || o.Body["unitsReleasedAt"] == nil {
		t.Fatalf("rejected, with the reason, and the release recorded: %s", o.Raw)
	}
	if e.onHandUnits(sku) != 5 {
		t.Fatal("the units are back")
	}
	if c := e.get("/api/slips/"+id, ana.Token); c.str("status") != "Rejected" {
		t.Fatalf("the rejection is committed on the slip: %s", c.Raw)
	}
}

func TestRefundAfterSaleReturnsTheUnitsButKeepsTheSlip(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(5)
	ana := e.register()
	id := e.enter(ana, sku, "COL-1", 2).str("id")
	e.drain()
	if r := e.post("/api/orders/"+id+"/cancellation", nil, ana.Token); r.Status != 202 {
		t.Fatalf("refund: %d %s", r.Status, r.Raw)
	}
	e.drain()
	if status(e, ana, id) != "Cancelled" || e.onHandUnits(sku) != 5 {
		t.Fatal("refunded and the units are back on the shelf")
	}
	if c := e.get("/api/slips/"+id, ana.Token); c.str("status") != "Issued" {
		t.Fatal("a refund does not roll back the slip that was issued")
	}
	if r := e.post("/api/orders/"+id+"/cancellation", nil, ana.Token); r.Status != 409 {
		t.Fatalf("a second refund is a conflict: %d", r.Status)
	}
}

func TestRefundBeatsAFinalMarkThatArrivesLate(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(5)
	ana := e.register()
	id := uuid.MustParse(e.enter(ana, sku, "COL-1", 1).str("id"))
	for pass := 0; pass < 4; pass++ {
		for _, row := range e.unsent() {
			if row.d.RoutingKey == messaging.IssuedKey {
				continue
			}
			e.markSent(row)
			_, _ = e.deliver(row.d.RoutingKey, row.d)
		}
	}
	if got := status(e, ana, id.String()); got != "Held" {
		t.Fatalf("held, slip issued, final mark not yet landed: %s", got)
	}
	if r := e.post("/api/orders/"+id.String()+"/cancellation", nil, ana.Token); r.Status != 202 {
		t.Fatal(r.Status)
	}
	e.drain()
	o := e.order(ana, id.String())
	if o.str("status") != "Cancelled" || o.Body["slipNumber"] != nil {
		t.Fatalf("the late final mark must not undo the refund: %s", o.Raw)
	}
	if c := e.get("/api/slips/"+id.String(), ana.Token); c.str("status") != "Issued" {
		t.Fatal("the issued slip stays")
	}
}

func TestADeliveryRepeatedByTheBrokerHasNoEffectTheSecondTime(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(5)
	ana := e.register()
	e.enter(ana, sku, "COL-1", 1)
	rows := e.unsent()
	if len(rows) != 1 {
		t.Fatal("one reserve command")
	}
	d := rows[0].d
	for i := 0; i < 3; i++ {
		if _, err := e.deliver(d.RoutingKey, d); err != nil {
			t.Fatal(err)
		}
	}
	if e.count(`SELECT count(*) FROM inventory.unit_holds`) != 1 || e.outbox("inventory", messaging.HeldKey) != 1 || e.onHandUnits(sku) != 4 {
		t.Fatal("at-least-once delivery, exactly-once effect: the inbox absorbs the repeats")
	}
}

func TestAFinalMarkBeforeTheHeldMarkIsRetriedNotSkuped(t *testing.T) {
	e := newEnv(t)
	sku := e.openSku(5)
	ana := e.register()
	id := uuid.MustParse(e.enter(ana, sku, "COL-1", 1).str("id"))
	_, err := e.app.Orders.MarkDispatched(context.Background(), uuid.New(), messaging.SlipIssued{OrderID: id, Number: "PS-TEST"}, 0)
	var ooo *messaging.OutOfOrderError
	if err == nil || !errorsAs(err, &ooo) {
		t.Fatalf("the final mark must ask to be retried, got %v", err)
	}
	if status(e, ana, id.String()) != "Requested" {
		t.Fatal("and change nothing")
	}
	e.drain()
	if status(e, ana, id.String()) != "Dispatched" {
		t.Fatal("the normal flow still completes")
	}
}

func TestIssueIsIdempotentAndPoisonIsAFlagNotAnException(t *testing.T) {
	e := newEnv(t)
	id, owner := uuid.New(), uuid.New()
	msg := uuid.New()
	held := messaging.StockHeld{OrderID: id, OwnerID: owner, SkuID: uuid.New(), Units: 1, Reference: "COL-1"}
	first, err := e.app.Slips.Issue(context.Background(), msg, held)
	again, err2 := e.app.Slips.Issue(context.Background(), msg, held)
	if err != nil || err2 != nil || first != dispatch.Issued || again != dispatch.Duplicate {
		t.Fatalf("issue then duplicate: %v %v %v %v", first, err, again, err2)
	}
	poison := messaging.StockHeld{OrderID: uuid.New(), OwnerID: owner, SkuID: uuid.New(), Units: 1, Reference: "xPOISONx"}
	if out, _ := e.app.Slips.Issue(context.Background(), uuid.New(), poison); out != dispatch.Rejected {
		t.Fatal("a poison reference is rejected")
	}
	if e.outbox("dispatch", messaging.ReleaseKey) != 1 || e.outbox("dispatch", messaging.RejectedKey) != 1 || e.outbox("dispatch", messaging.IssuedKey) != 1 {
		t.Fatal("one issued, one release command and one rejection")
	}
}
