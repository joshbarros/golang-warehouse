package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-warehouse/internal/inventory"
	"github.com/joshbarros/golang-warehouse/internal/platform/messaging"
)

func (e *env) rawSku(units int) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO inventory.skus (id, code, name, category, bin, received_at, received_units, on_hand_units, created_at)
		VALUES ($1,$2,'a','b','c',now(),$3,$3,now())`, id, code(), units)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func reserve(e *env, sku uuid.UUID, units int) inventory.ReserveOutcome {
	out, err := e.app.Skus.Reserve(context.Background(), uuid.New(), messaging.ReserveStock{OrderID: uuid.New(), OwnerID: uuid.New(), SkuID: sku, Units: units, Reference: "R"})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestConcurrentReservesNeverOversellAndAlwaysSumToTheStock(t *testing.T) {
	e := newEnv(t)
	sku := e.rawSku(30)
	var wg sync.WaitGroup
	var mu sync.Mutex
	held := 0
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reserve(e, sku, 1) == inventory.Held {
				mu.Lock()
				held++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var left int
	_ = e.pool.QueryRow(context.Background(), `SELECT on_hand_units FROM inventory.skus WHERE id = $1`, sku).Scan(&left)
	if held != 30 || left != 0 {
		t.Fatalf("60 orders, 30 units: exactly 30 held and none left, got %d held and %d left", held, left)
	}
	if e.count(`SELECT count(*) FROM inventory.unit_holds WHERE sku_id = $1`, sku) != 30 {
		t.Fatal("one hold per success")
	}
}

func TestAShortReserveWritesNoHoldAndTakesNothing(t *testing.T) {
	e := newEnv(t)
	sku := e.rawSku(3)
	if reserve(e, sku, 2) != inventory.Held || reserve(e, sku, 2) != inventory.Short {
		t.Fatal("2 of 3 held, then 2 wanted and 1 left is short")
	}
	var left int
	_ = e.pool.QueryRow(context.Background(), `SELECT on_hand_units FROM inventory.skus WHERE id = $1`, sku).Scan(&left)
	if left != 1 || e.count(`SELECT count(*) FROM inventory.unit_holds WHERE sku_id = $1`, sku) != 1 {
		t.Fatalf("the short try left no trace: %d left", left)
	}
	if reserve(e, sku, 1) != inventory.Held {
		t.Fatal("the last unit is still there")
	}
}

func TestReleaseGivesBackExactlyOnce(t *testing.T) {
	e := newEnv(t)
	sku := e.rawSku(5)
	order := uuid.New()
	if out, _ := e.app.Skus.Reserve(context.Background(), uuid.New(), messaging.ReserveStock{OrderID: order, OwnerID: uuid.New(), SkuID: sku, Units: 2, Reference: "R"}); out != inventory.Held {
		t.Fatal(out)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.app.Skus.Release(context.Background(), uuid.New(), messaging.ReleaseStock{OrderID: order, Reason: "refund"}); err != nil {
			t.Fatal(err)
		}
	}
	var left int
	_ = e.pool.QueryRow(context.Background(), `SELECT on_hand_units FROM inventory.skus WHERE id = $1`, sku).Scan(&left)
	if left != 5 || e.outbox("inventory", messaging.ReleasedKey) != 1 {
		t.Fatalf("given back once, not three times: %d left", left)
	}
}

func TestOpeningASkuValidatesIt(t *testing.T) {
	e := newEnv(t)
	adm := e.admin()
	good := func() map[string]any {
		return map[string]any{"code": code(), "name": "a", "category": "b", "bin": "c", "receivedUnits": 10}
	}
	if r := e.post("/api/skus", good(), adm.Token); r.Status != 201 || r.Body["onHandUnits"] != float64(10) {
		t.Fatalf("a sku opens full: %d %s", r.Status, r.Raw)
	}
	for name, mut := range map[string]func(map[string]any){
		"no code":   func(m map[string]any) { m["code"] = "" },
		"long code": func(m map[string]any) { m["code"] = "ABCDEFGHIJKLMNOPQRSTU" },
		"no text":   func(m map[string]any) { m["category"] = " " },
		"zero":      func(m map[string]any) { m["receivedUnits"] = 0 },
		"too many":  func(m map[string]any) { m["receivedUnits"] = 100_000_000 },
	} {
		bad := good()
		mut(bad)
		if r := e.post("/api/skus", bad, adm.Token); r.Status != 400 {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	dup := good()
	e.post("/api/skus", dup, adm.Token)
	if r := e.post("/api/skus", dup, adm.Token); r.Status != 409 {
		t.Fatalf("the same code twice: %d", r.Status)
	}
	if r := e.post("/api/skus", good(), e.register().Token); r.Status != 403 {
		t.Fatalf("a customer cannot open a sku: %d", r.Status)
	}
}
