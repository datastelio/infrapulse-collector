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

// DCGM's dcgm_fields.h is the reference: uncorrectable remapped rows is field
// 393 and correctable 394, and the violation counters are served in ns.
func TestIDsAndViolationUnits(t *testing.T) {
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
		f, ok := Lookup(name)
		if !ok || f.NameScale() != 1e-3 || f.Unit != "us" {
			t.Errorf("%s: scale %v unit %q, want ns converted to us", name, f.NameScale(), f.Unit)
		}
		if !strings.Contains(f.ExporterHelp(), "(in ns)") {
			t.Errorf("%s: the exporter HELP line must give the served unit, got %q", name, f.ExporterHelp())
		}
	}
	if f, _ := Lookup("DCGM_FI_DEV_GPU_UTIL"); f.NameScale() != 1 || f.ExporterHelp() != f.Help {
		t.Error("a field served in its catalog unit must come back unscaled")
	}
}
