package fields

import "testing"

func TestLookupAMDSelectsByLabel(t *testing.T) {
	cases := []struct {
		metric, clockType string
		want              []string
	}{
		{"gpu_clock", "system", []string{"sm_clock_mhz"}},
		{"GPU_CLOCK", "GPU_CLOCK_TYPE_MEMORY", []string{"mem_clock_mhz"}}, // upper case and enum spelling
		{"gpu_clock", "soc", nil},
		{"gpu_umc_activity", "", []string{"mem_copy_util_pct", "dram_active"}},
		{"gpu_max_clock", "system", []string{"max_sm_clock_mhz"}},
	}
	for _, c := range cases {
		got := LookupAMD(c.metric, map[string]string{"clock_type": c.clockType})
		if len(got) != len(c.want) {
			t.Fatalf("%s{%s}: got %v, want %v", c.metric, c.clockType, got, c.want)
		}
		for i := range got {
			if got[i].Column != c.want[i] {
				t.Errorf("%s{%s}: column %q, want %q", c.metric, c.clockType, got[i].Column, c.want[i])
			}
		}
	}
	if IsAMDMetric("DCGM_FI_DEV_GPU_UTIL") || !IsAMDMetric("gpu_gfx_activity") {
		t.Error("IsAMDMetric")
	}
}

func TestAMDCatalogColumnsAreCanonical(t *testing.T) {
	cols := map[string]bool{}
	for _, c := range Columns() {
		cols[c] = true
	}
	for _, f := range AMDCatalog {
		if !cols[f.Column] && f.Column != AMDPowerViolationPct && f.Column != AMDThermalViolationPct && f.Column != AMDHBMViolationPct {
			t.Errorf("%s maps to %q, not a gpu_samples column", f.Name, f.Column)
		}
		if f.Scale == 0 {
			t.Errorf("%s has no scale", f.Name)
		}
	}
}

func TestAMDReasons(t *testing.T) {
	if _, ok := AMDReasons(0, 0, 0, false, false, false); ok {
		t.Error("no violation metrics must leave the reasons empty (virtualised GPU), not zero")
	}
	if m, ok := AMDReasons(0, 0, 0, true, true, true); !ok || m != 0 {
		t.Errorf("no throttling: got %#x %v", m, ok)
	}
	if m, _ := AMDReasons(97, 10, 0, true, true, true); m != ReasonSWPowerCap {
		t.Errorf("power: got %#x", m)
	}
	if m, _ := AMDReasons(0, 60, 80, true, true, true); m != ReasonSWThermalSlowdown|ReasonHWThermalSlowdown {
		t.Errorf("thermal: got %#x", m)
	}
	if m, _ := AMDReasons(49, 0, 0, true, false, false); m != 0 {
		t.Errorf("below half the interval is not throttled: got %#x", m)
	}
}

func TestVendorAndModelName(t *testing.T) {
	for model, want := range map[string]string{
		"AMD Instinct MI300X": "amd", "MI325X": "amd", "NVIDIA H100 80GB HBM3": "nvidia", "MIG 1g.10gb": "", "": "",
	} {
		if got := Vendor(model); got != want {
			t.Errorf("Vendor(%q) = %q, want %q", model, got, want)
		}
	}
	for card, want := range map[string]string{
		"AMD Instinct MI300X": "AMD Instinct MI300X", "MI300X": "AMD MI300X", "0x74A1": "AMD Instinct MI300X", "": "",
	} {
		if got := AMDModelName(card); got != want {
			t.Errorf("AMDModelName(%q) = %q, want %q", card, got, want)
		}
	}
}
