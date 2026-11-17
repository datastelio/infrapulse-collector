package amd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/sample"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func values(s sample.Sample) string {
	return strings.Join([]string{s.UtilGPU, s.UtilMem, s.MemUsed, s.MemTotal, s.Temp, s.Power, s.PowerLimit, s.ClockSM, s.ClockSMMax, s.Throttle}, ",")
}

func TestParseAMDSMI61(t *testing.T) {
	static := ParseAMDSMIStatic(read(t, "amd-smi-list.json"), read(t, "amd-smi-static.json"))
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	got, err := ParseAMDSMI(read(t, "amd-smi-metric-6.1.json"), static, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2", len(got))
	}
	g := got[0]
	if g.Vendor != "amd" || g.Index != 0 || g.Model != "AMD Instinct MI300X" ||
		g.ID != "00000000-0000-4000-8000-0000000000a0" || !g.Time.Equal(now) {
		t.Errorf("identity = %+v", g)
	}
	// edge temperature is N/A, so hotspot is used.
	if v := values(g); v != "88,35,120000,196592,71,512,750,1980,2100,throttled" {
		t.Errorf("gpu 0 values = %s", v)
	}
	// Plain numbers and "140 W" strings; missing clock and power limit stay empty.
	if v := values(got[1]); v != "0,0,283,196592,38,140,,,,none" {
		t.Errorf("gpu 1 values = %s", v)
	}
}

func TestParseAMDSMI64Wrapped(t *testing.T) {
	got, err := ParseAMDSMI(read(t, "amd-smi-metric-6.4.json"), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Index != 3 || got[0].Model != "" || got[0].ID != "" {
		t.Fatalf("got %+v", got)
	}
	// GB and B units convert to MiB; upper-case keys match.
	if v := values(got[0]); v != "54,,16384,65536,52,301.5,550,1700,1700," {
		t.Errorf("values = %s", v)
	}
}

func TestParseAMDSMIBadJSON(t *testing.T) {
	if _, err := ParseAMDSMI([]byte("not json"), nil, time.Now()); err == nil {
		t.Error("want error")
	}
}

func TestParseAMDSMIStaticTolerant(t *testing.T) {
	if m := ParseAMDSMIStatic(nil, []byte("garbage")); len(m) != 0 {
		t.Errorf("got %v", m)
	}
}

func TestParseROCmSMI(t *testing.T) {
	static := ParseROCmSMIStatic(read(t, "rocm-smi-static.json"))
	got, err := ParseROCmSMI(read(t, "rocm-smi.json"), static, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2 (system entry skipped)", len(got))
	}
	g := got[0]
	if g.Index != 0 || g.Model != "AMD Instinct MI250X" || g.ID != "0x0000000000000a00" {
		t.Errorf("identity = %+v", g)
	}
	if v := values(g); v != "76,22,26208,65520,47,245,560,1700,," {
		t.Errorf("card0 values = %s", v)
	}
	// Older key names, junction fallback, power limit from the sample itself.
	if v := values(got[1]); v != "0,0,,,39,88,500,,," {
		t.Errorf("card1 values = %s", v)
	}
}

func TestNumber(t *testing.T) {
	cases := []struct {
		in   any
		f    float64
		unit string
		ok   bool
	}{
		{12.5, 12.5, "", true},
		{"120 W", 120, "W", true},
		{"(1500Mhz)", 1500, "Mhz", true},
		{"N/A", 0, "", false},
		{map[string]any{"value": 64.0, "unit": "GB"}, 64, "GB", true},
		{map[string]any{"value": "N/A", "unit": "W"}, 0, "W", false},
		{nil, 0, "", false},
	}
	for _, c := range cases {
		f, unit, ok := number(c.in)
		if f != c.f || unit != c.unit || ok != c.ok {
			t.Errorf("number(%v) = %v %q %v", c.in, f, unit, ok)
		}
	}
}
