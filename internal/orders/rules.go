// Package orders is the HTTP side of the saga and the handler that moves an order forward as events arrive.
package orders

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Order rules.
const (
	MaxUnitsPerOrder   = 50
	MaxReferenceLength = 40
	FeeCentsPerUnit    = 150
	FlatFeeCents       = 400
)

// ErrOverflow is returned instead of wrapping.
var ErrOverflow = errors.New("arithmetic overflow")

// Validate returns an error message, or "" when the request is acceptable. The unit count is compared as int64 on purpose:
// narrowing 4294967297 to int32 would wrap to 1 and pass.
func Validate(reference string, units int64) string {
	trimmed := strings.TrimSpace(reference)
	switch {
	case trimmed == "":
		return "Order reference is required."
	case len([]rune(trimmed)) > MaxReferenceLength:
		return fmt.Sprintf("Order reference must be at most %d characters.", MaxReferenceLength)
	case units < 1:
		return "An order needs at least 1 unit."
	case units > MaxUnitsPerOrder:
		return fmt.Sprintf("An order takes at most %d units.", MaxUnitsPerOrder)
	}
	return ""
}

// FeeCents is a fee per unit plus a flat fee, in cents. Overflow is an error, not a wrap.
func FeeCents(units int) (int64, error) {
	if units < 0 || int64(units) > (math.MaxInt64-FlatFeeCents)/FeeCentsPerUnit {
		return 0, ErrOverflow
	}
	return int64(units)*FeeCentsPerUnit + FlatFeeCents, nil
}
