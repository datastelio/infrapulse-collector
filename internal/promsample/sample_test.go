package promsample

import (
	"strings"
	"testing"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/promtext"
)

// One MI300X as the AMD Device Metrics Exporter serves it (label set from the
// exporter's docs; values made up for the test).
const amdText = `# HELP gpu_gfx_activity GFX activity
gpu_gfx_activity{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 87
gpu_umc_activity{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 35
gpu_used_vram{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 150000
gpu_power_usage{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 640
gpu_package_power{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 655
gpu_energy_consumed{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 5000000
gpu_clock{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0",clock_type="system",clock_index="0"} 2000
gpu_clock{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0",clock_type="system",clock_index="1"} 2100
gpu_clock{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0",clock_type="memory",clock_index="0"} 1300
gpu_clock{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0",clock_type="soc",clock_index="0"} 1100
gpu_violation_ppt_residency_percentage{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 80
gpu_violation_socket_thermal_residency_percentage{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0"} 0
gpu_prof_tensor_active_percent{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0",job_id="train-7"} 42
gpu_xgmi_link_rx{card_model="0x74a1",gpu_id="3",serial_number="PCB123",hostname="node-a",gpu_partition_id="0",link_index="0"} 9
gpu_clock{card_model="0x74a1",gpu_id="3",serial_number="PCB124",hostname="node-a",gpu_partition_id="2",clock_type="system"} 1500
`

func TestBuildFromAMDExporter(t *testing.T) {
	ss, err := promtext.Parse(strings.NewReader(amdText))
	if err != nil {
		t.Fatal(err)
	}
	rows, unknown := Build(time.Unix(1_700_000_000, 0), ss, Options{})
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (GPU 3 and its compute partition 3/2), got %d", len(rows))
	}
	r := rows[0]
	if r.Identity["gpu_index"] != "3" || r.Identity["host"] != "node-a" || r.Identity["gpu_uuid"] != "PCB123" ||
		r.Identity["model"] != "AMD Instinct MI300X" || r.Identity["job_id"] != "train-7" {
		t.Errorf("identity: %v", r.Identity)
	}
	want := map[string]float64{
		"gpu_util_pct":        87,
		"mem_copy_util_pct":   35,
		"dram_active":         0.35, // percent -> ratio
		"fb_used_mib":         150000,
		"power_w":             640,  // gpu_power_usage wins over gpu_package_power
		"energy_mj":           5000, // uJ -> mJ
		"sm_clock_mhz":        2050, // mean of the per-XCD system clocks; soc ignored
		"mem_clock_mhz":       1300,
		"tensor_active":       0.42,
		"clock_event_reasons": 4, // PPT residency 80% -> sw_power_cap
	}
	for col, v := range want {
		if got, ok := r.Values[col]; !ok || got != v {
			t.Errorf("%s = %v (set %v), want %v", col, got, ok, v)
		}
	}
	if rows[1].Identity["gpu_index"] != "3/2" {
		t.Errorf("partition row: %v", rows[1].Identity)
	}
	if _, ok := rows[1].Values["clock_event_reasons"]; ok {
		t.Error("a GPU without violation metrics must not get a reasons value")
	}
	if len(unknown) != 0 { // gpu_xgmi_link_rx is an AMD metric we don't read, not an unknown one
		t.Errorf("unknown: %v", unknown)
	}
}

func TestPackagePowerWhenPowerUsageMissing(t *testing.T) {
	ss, _ := promtext.Parse(strings.NewReader(`gpu_package_power{gpu_id="0",hostname="h",card_model="AMD Instinct MI300X"} 512` + "\n"))
	rows, _ := Build(time.Now(), ss, Options{})
	if len(rows) != 1 || rows[0].Values["power_w"] != 512 {
		t.Fatalf("rows: %+v", rows)
	}
}

// dcgm-exporter serves the violation counters in nanoseconds; the column is in
// microseconds.
func TestBuildConvertsViolationCounters(t *testing.T) {
	const text = `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-1",modelName="NVIDIA A100-SXM4-40GB",Hostname="node1"} 97
DCGM_FI_DEV_POWER_VIOLATION{gpu="0",UUID="GPU-1",modelName="NVIDIA A100-SXM4-40GB",Hostname="node1"} 2.5e+12
`
	samples, err := promtext.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := Build(time.Unix(1_700_000_000, 0), samples, Options{})
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if got := rows[0].Values["power_violation_us"]; got != 2.5e9 {
		t.Errorf("power_violation_us = %v, want 2.5e9 (2.5e12 ns)", got)
	}
	if got := rows[0].Values["gpu_util_pct"]; got != 97 {
		t.Errorf("gpu_util_pct = %v, want 97 unscaled", got)
	}
}
