package inventory

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-warehouse/internal/platform/httpx"
)

const maxReceivedUnits = 100_000

// Sku is the public view of a sku.
type Sku struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Category      string    `json:"category"`
	Bin           string    `json:"bin"`
	ReceivedAt    time.Time `json:"receivedAt"`
	ReceivedUnits int       `json:"receivedUnits"`
	OnHandUnits   int       `json:"onHandUnits"`
}

// Info is what other modules may ask the skus module (Orders never reads this schema).
type Info struct {
	ID   uuid.UUID
	Code string
}

// Catalog answers the one cross-module read: a sku's existence.
type Catalog struct{ Pool *pgxpool.Pool }

// Find looks a sku up by code. It returns nil when there is none.
func (c Catalog) Find(ctx context.Context, code string) (*Info, error) {
	var i Info
	err := c.Pool.QueryRow(ctx, `SELECT id, code FROM inventory.skus WHERE code = $1`, code).Scan(&i.ID, &i.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &i, err
}

// API is the skus HTTP surface.
type API struct {
	Pool *pgxpool.Pool
	Auth httpx.Authenticator
	Log  *slog.Logger
}

// Routes registers the sku endpoints; every one needs a signed-in user.
func (a *API) Routes(mux *http.ServeMux) {
	authed := func(h httpx.HandlerFunc) http.HandlerFunc {
		return httpx.Authenticated(a.Log, a.Auth, httpx.Wrap(a.Log, h))
	}
	mux.HandleFunc("GET /api/skus", authed(a.list))
	mux.HandleFunc("GET /api/skus/{code}", authed(a.get))
	mux.HandleFunc("POST /api/skus", httpx.Authenticated(a.Log, a.Auth, httpx.Staff(httpx.Wrap(a.Log, a.create))))
}

const skuSelect = `SELECT id, code, name, category, bin, received_at, received_units, on_hand_units FROM inventory.skus`

func scanSku(row pgx.Row) (Sku, error) {
	var d Sku
	err := row.Scan(&d.ID, &d.Code, &d.Name, &d.Category, &d.Bin, &d.ReceivedAt, &d.ReceivedUnits, &d.OnHandUnits)
	return d, err
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.Pool.Query(r.Context(), skuSelect+` ORDER BY received_at, code`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Sku{}
	for rows.Next() {
		d, err := scanSku(rows)
		if err != nil {
			return err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	d, err := scanSku(a.Pool.QueryRow(r.Context(), skuSelect+` WHERE code = $1`, r.PathValue("code")))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Fail(http.StatusNotFound, "Not found.")
	}
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}

type createRequest struct {
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Category      string     `json:"category"`
	Bin           string     `json:"bin"`
	ReceivedAt    *time.Time `json:"receivedAt"`
	ReceivedUnits int64      `json:"receivedUnits"`
}

// create opens a sku. Staff only. This is the only place on_hand_units is initialised; after that only the reserve and release
// handlers write it.
func (a *API) create(w http.ResponseWriter, r *http.Request) error {
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > 20 {
		return httpx.Fail(http.StatusBadRequest, "Code is required and at most 20 characters.")
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Category) == "" || strings.TrimSpace(req.Bin) == "" {
		return httpx.Fail(http.StatusBadRequest, "Name, category and bin are required.")
	}
	if req.ReceivedUnits < 1 || req.ReceivedUnits > maxReceivedUnits {
		return httpx.Fail(http.StatusBadRequest, "Units received must be between 1 and %d units.", maxReceivedUnits)
	}
	when := time.Now().AddDate(0, 0, 14)
	if req.ReceivedAt != nil {
		when = *req.ReceivedAt
	}
	id, _ := uuid.NewV7()
	_, err := a.Pool.Exec(r.Context(), `
		INSERT INTO inventory.skus (id, code, name, category, bin, received_at, received_units, on_hand_units, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7,now())`,
		id, code, strings.TrimSpace(req.Name), strings.TrimSpace(req.Category), strings.TrimSpace(req.Bin), when, req.ReceivedUnits)
	if err != nil {
		var coder interface{ SQLState() string }
		if errors.As(err, &coder) && coder.SQLState() == "23505" {
			return httpx.Fail(http.StatusConflict, "Sku '%s' already exists.", code)
		}
		return err
	}
	d, err := scanSku(a.Pool.QueryRow(r.Context(), skuSelect+` WHERE id = $1`, id))
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/skus/"+code)
	return httpx.JSON(w, http.StatusCreated, d)
}
