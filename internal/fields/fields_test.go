package fields

import (
	"os"
	"strings"
	"testing"
)

func TestCatalogUnique(t *testing.T) {
	ids, names, cols := map[int]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range Catalog {
		if ids[f.ID] || names[f.Name] || cols[f.Column] {
			t.Errorf("duplicate field %+v", f)
		}
		ids[f.ID], names[f.Name], cols[f.Column] = true, true, true
	}
}

func TestAliasLookup(t *testing.T) {
	a, ok1 := Lookup("DCGM_FI_DEV_CLOCK_THROTTLE_REASONS")
	b, ok2 := Lookup("DCGM_FI_DEV_CLOCKS_EVENT_REASONS")
	if !ok1 || !ok2 || a.Column != b.Column {
		t.Fatal("throttle reason alias not resolved")
	}
}

// The checked-in field file must match the catalog. Regenerate with:
//
//	go run ./cmd/ipcollect fields --dcgm-exporter-csv > fields/infrapulse-dcgm-fields.csv
func TestCheckedInCSVMatches(t *testing.T) {
	b, err := os.ReadFile("../../fields/infrapulse-dcgm-fields.csv")
	if err != nil {
		t.Skip("field file not generated yet")
	}
	if string(b) != DCGMExporterCSV() {
		t.Fatal("fields/infrapulse-dcgm-fields.csv is stale; regenerate it")
	}
}

func TestDCGM47Names(t *testing.T) {
	seen := map[string]string{}
	for _, f := range Catalog {
		if f.V2 == "" {
			if f.Column != "clock_event_reasons" {
				t.Errorf("%s has no DCGM 4.7 name; only the throttle-reasons bitmask may lack one", f.Name)
			}
			continue
		}
		if f.V2Scale <= 0 || f.V2Help == "" {
			t.Errorf("%s: V2Scale and V2Help must be set", f.V2)
		}
		if prev, dup := seen[f.V2]; dup {
			t.Errorf("%s is the 4.7 name of both %s and %s", f.V2, prev, f.Name)
		}
		seen[f.V2] = f.Name
		g, scale, v2, ok := LookupScaled(f.V2)
		if !ok || g.Column != f.Column {
			t.Errorf("LookupScaled(%s) = %s, %v", f.V2, g.Column, ok)
		}
		if f.V2 != f.Name && (!v2 || scale != f.V2Scale) {
			t.Errorf("LookupScaled(%s): scale %v v2 %v, want %v true", f.V2, scale, v2, f.V2Scale)
		}
		if f.V2 == f.Name && f.V2Scale != 1 {
			t.Errorf("%s kept its name in DCGM 4.7 but has scale %v", f.Name, f.V2Scale)
		}
	}
	if _, scale, v2, ok := LookupScaled("DCGM_FI_DEV_GPU_UTIL"); !ok || scale != 1 || v2 {
		t.Error("a classic name must come back unscaled")
	}
}

func TestCheckedInCSV49Matches(t *testing.T) {
	b, err := os.ReadFile("../../fields/infrapulse-dcgm-fields-4.9.csv")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != DCGMExporterCSVV2() {
		t.Fatal("fields/infrapulse-dcgm-fields-4.9.csv is stale; run `make fields`")
	}
}

// DCGM's dcgm_fields.h is the reference: uncorrectable remapped rows is field
// 393 and correctable 394, and the violation counters are served in ns.
func TestClassicIDsAndViolationUnits(t *testing.T) {
	ids := map[string]int{
		"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS": 393,
		"DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS":   394,
	}
	for name, id := range ids {
		if f, ok := Lookup(name); !ok || f.ID != id {
			t.Errorf("%s has ID %d, want %d", name, f.ID, id)
		}
	}
	for _, name := range []string{
		"DCGM_FI_DEV_POWER_VIOLATION", "DCGM_FI_DEV_THERMAL_VIOLATION", "DCGM_FI_DEV_SYNC_BOOST_VIOLATION",
		"DCGM_FI_DEV_BOARD_LIMIT_VIOLATION", "DCGM_FI_DEV_RELIABILITY_VIOLATION",
	} {
		f, scale, v2, ok := LookupScaled(name)
		if !ok || v2 || scale != 1e-3 || f.Unit != "us" {
			t.Errorf("%s: scale %v unit %q v2 %v, want ns converted to us", name, scale, f.Unit, v2)
		}
		if !strings.Contains(f.ExporterHelp(), "(in ns)") {
			t.Errorf("%s: the exporter HELP line must give the served unit, got %q", name, f.ExporterHelp())
		}
		// The same second of throttling must give the same column value from either exporter generation.
		if got := 1e9 * scale; got != 1*f.V2Scale {
			t.Errorf("%s: 1 s is %v us under the classic name and %v us under the 4.7 name", name, got, f.V2Scale)
		}
	}
}
