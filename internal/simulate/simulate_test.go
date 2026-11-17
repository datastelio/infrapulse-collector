package simulate

import (
	"context"
	"strconv"
	"testing"
)

func TestQuery(t *testing.T) {
	for _, vendor := range []string{"nvidia", "amd"} {
		got, err := New(vendor).Query(context.Background())
		if err != nil || len(got) != 2 {
			t.Fatalf("%s: %d samples, err %v", vendor, len(got), err)
		}
		for _, s := range got {
			u, _ := strconv.ParseFloat(s.UtilGPU, 64)
			p, _ := strconv.ParseFloat(s.Power, 64)
			l, _ := strconv.ParseFloat(s.PowerLimit, 64)
			if s.Vendor != vendor || u < 0 || u > 100 || p <= 0 || p > l || s.ID == "" || s.Throttle == "" {
				t.Errorf("%s: implausible sample %+v", vendor, s)
			}
		}
	}
}
