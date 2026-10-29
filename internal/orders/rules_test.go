package orders

import (
	"math"
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	long := strings.Repeat("x", MaxReferenceLength+1)
	cases := []struct {
		reference string
		units     int64
		want      string
	}{
		{"REF-1", 1, ""},
		{"  REF-1  ", MaxUnitsPerOrder, ""},
		{strings.Repeat("x", MaxReferenceLength), 1, ""},
		{"", 1, "Order reference is required."},
		{"   ", 1, "Order reference is required."},
		{long, 1, "Order reference must be at most 40 characters."},
		{"REF-1", 0, "An order needs at least 1 unit."},
		{"REF-1", -3, "An order needs at least 1 unit."},
		{"REF-1", MaxUnitsPerOrder + 1, "An order takes at most 50 units."},
		{"REF-1", 4294967297, "An order takes at most 50 units."},
		{"REF-1", math.MaxInt64, "An order takes at most 50 units."},
	}
	for _, c := range cases {
		if got := Validate(c.reference, c.units); got != c.want {
			t.Errorf("Validate(%q, %d) = %q, want %q", c.reference, c.units, got, c.want)
		}
	}
}

func TestFeeIsAFeePerUnitPlusAFlatFee(t *testing.T) {
	for _, units := range []int{1, 2, MaxUnitsPerOrder} {
		got, err := FeeCents(units)
		if want := int64(units)*FeeCentsPerUnit + FlatFeeCents; err != nil || got != want {
			t.Errorf("FeeCents(%d) = %d, %v; want %d", units, got, err, want)
		}
	}
}

func TestFeeThrowsInsteadOfWrapping(t *testing.T) {
	for _, units := range []int{math.MaxInt32, -1} {
		if _, err := FeeCents(units); units < 0 && err == nil {
			t.Errorf("FeeCents(%d) must fail", units)
		}
	}
}
