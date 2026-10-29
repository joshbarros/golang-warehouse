package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-warehouse/internal/platform/messaging"
	"github.com/joshbarros/golang-warehouse/internal/platform/pgdb"
)

// Schema and consumer names.
const (
	Schema       = "inventory"
	ConsumerName = "inventory"

	HoldHeld     = "Held"
	HoldReleased = "Released"
)

// ReserveOutcome is what a reserve did.
type ReserveOutcome int

// The ways a reserve can end.
const (
	Held ReserveOutcome = iota
	Short
	Duplicate
)

// Service is the skus module's logic. It is the only writer of on_hand_units.
type Service struct {
	Pool   *pgxpool.Pool
	Outbox messaging.Outbox
	Log    *slog.Logger
}

// NewService builds a Service with its outbox.
func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Pool: pool, Outbox: messaging.Outbox{Schema: Schema, Module: "inventory"}, Log: log}
}

// Handle implements messaging.Handler for the skus queue.
func (s *Service) Handle(ctx context.Context, d messaging.Delivery) error {
	switch d.RoutingKey {
	case messaging.ReserveKey:
		cmd, err := messaging.Parse[messaging.ReserveStock](d.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
		}
		_, err = s.Reserve(ctx, d.MessageID, cmd)
		return err
	case messaging.ReleaseKey:
		cmd, err := messaging.Parse[messaging.ReleaseStock](d.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
		}
		_, err = s.Release(ctx, d.MessageID, cmd)
		return err
	}
	return fmt.Errorf("skus does not handle %q", d.RoutingKey)
}

// Reserve holds units with one conditional UPDATE that matches only while the remaining units still cover the request: when
// two orders fight for the last unit, one commits a hold and the other is short and writes none.
func (s *Service) Reserve(ctx context.Context, messageID uuid.UUID, cmd messaging.ReserveStock) (ReserveOutcome, error) {
	outcome := Held
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil {
			return err
		}
		if !first {
			outcome = Duplicate
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE inventory.skus SET on_hand_units = on_hand_units - $2 WHERE id = $1 AND on_hand_units >= $2`, cmd.SkuID, cmd.Units)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			if _, err := tx.Exec(ctx, `INSERT INTO inventory.unit_holds (order_id, owner_id, sku_id, units, status, created_at) VALUES ($1,$2,$3,$4,'Held',now())`,
				cmd.OrderID, cmd.OwnerID, cmd.SkuID, cmd.Units); err != nil {
				return err
			}
			return s.Outbox.Enqueue(ctx, tx, messaging.HeldKey, cmd.OrderID,
				messaging.StockHeld{OrderID: cmd.OrderID, OwnerID: cmd.OwnerID, SkuID: cmd.SkuID, Units: cmd.Units, Reference: cmd.Reference})
		}
		outcome = Short
		var left int
		_ = tx.QueryRow(ctx, `SELECT on_hand_units FROM inventory.skus WHERE id = $1`, cmd.SkuID).Scan(&left)
		return s.Outbox.Enqueue(ctx, tx, messaging.ShortKey, cmd.OrderID,
			messaging.StockShort{OrderID: cmd.OrderID, Reference: cmd.Reference, RequestedUnits: cmd.Units, OnHandUnits: left})
	})
	return outcome, err
}

// Release gives held units back exactly once. A second release for the same order changes nothing.
func (s *Service) Release(ctx context.Context, messageID uuid.UUID, cmd messaging.ReleaseStock) (bool, error) {
	released := false
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil || !first {
			return err
		}
		var skuID uuid.UUID
		var units int
		err = tx.QueryRow(ctx, `
			UPDATE inventory.unit_holds SET status = 'Released', released_at = now(), release_reason = $2
			WHERE order_id = $1 AND status = 'Held' RETURNING sku_id, units`, cmd.OrderID, cmd.Reason).Scan(&skuID, &units)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing held, or already released
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.skus SET on_hand_units = on_hand_units + $2 WHERE id = $1 AND on_hand_units + $2 <= received_units`, skuID, units); err != nil {
			return err
		}
		released = true
		return s.Outbox.Enqueue(ctx, tx, messaging.ReleasedKey, cmd.OrderID, messaging.StockReleased{OrderID: cmd.OrderID, Units: units, Reason: cmd.Reason})
	})
	return released, err
}
