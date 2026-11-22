package fields

import (
	"os"
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
