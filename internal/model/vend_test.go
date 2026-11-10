package model

import (
	"errors"
	"testing"
)

func TestDefaultSlotMapMatchesProbe(t *testing.T) {
	if got := DefaultSlotMap().String(); got != "1=AA 2=BA 3=AB 4=BB 5=AC 6=BC" {
		t.Fatalf("slot map: %s", got)
	}
}

func TestParseSlotMap(t *testing.T) {
	m, err := ParseSlotMap(map[string]string{"1": "aa", " 2 ": "BA"})
	if err != nil || m[1] != "AA" || m[2] != "BA" {
		t.Fatalf("%v %v", m, err)
	}
	if m, _ := ParseSlotMap(nil); m.String() != DefaultSlotMap().String() {
		t.Fatal("empty means the default map")
	}
	for _, bad := range []map[string]string{
		{"0": "AA"}, {"x": "AA"}, {"1": "ZA"}, {"1": "AG"}, {"1": "A"}, {"1": "AA", "2": "AA"},
	} {
		if _, err := ParseSlotMap(bad); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}

func TestCheckSimMode(t *testing.T) {
	for _, m := range SimModes {
		if err := CheckSimMode(m); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	err := CheckSimMode("explode")
	if !errors.Is(err, ErrInvalidSimMode) || err.Error() != `simulator mode "explode": use ok, jam or no_ack` {
		t.Fatalf("%v", err)
	}
}
