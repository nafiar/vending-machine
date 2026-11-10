package catalog

import (
	"testing"
	"time"
)

func TestPassed0300(t *testing.T) {
	loc := time.FixedZone("WIB", 7*3600)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	cases := []struct {
		fetched, now time.Time
		want         bool
	}{
		{at(23, 2, 0), at(23, 2, 59), false},
		{at(23, 2, 0), at(23, 3, 0), true},
		{at(23, 3, 1), at(24, 2, 0), false},
		{at(22, 23, 0), at(23, 9, 0), true},
	}
	for _, c := range cases {
		if got := passed0300(c.fetched, c.now); got != c.want {
			t.Errorf("fetched %s now %s: %v", c.fetched, c.now, got)
		}
	}
}

func TestValidUpload(t *testing.T) {
	for _, ok := range []string{"products/p1.png", "products/3f9c-e1.SVG", "a/b/c.webp"} {
		if !validUpload(ok) {
			t.Errorf("%s must be accepted", ok)
		}
	}
	for _, bad := range []string{"../config.json", "products/x.exe", "a/b/c/d.png", "products/../x.png", ""} {
		if validUpload(bad) {
			t.Errorf("%s must be refused", bad)
		}
	}
}
