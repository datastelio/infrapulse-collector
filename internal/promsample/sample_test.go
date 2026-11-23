package promsample

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/fields"
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

// dcgm-exporter 4.9.0 serves the DCGM 4.7 names in hertz, bytes, joules, seconds
// and 0 to 1 ratios, with the hostname and DCGM_FI_SYSTEM_DRIVER_VERSION labels.
const dcgm49 = `
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1",DCGM_FI_SYSTEM_DRIVER_VERSION="570.1"} 0.97
DCGM_FI_DEV_FB_USED_BYTES{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 42949672960
DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 1500
DCGM_FI_DEV_SM_CLOCK_HERTZ{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 1980000000
DCGM_FI_DEV_SM_MAX_CLOCK_HERTZ{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 1980000000
DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 612.5
DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 12.5
DCGM_FI_PROF_TENSOR_UTIL_RATIO{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 0.41
DCGM_FI_DEV_XID_ERROR{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 0
DCGM_FI_DEV_ECC_SBE_VOL_TOTAL{gpu="0",UUID="GPU-a",modelName="NVIDIA H100 80GB HBM3",hostname="node1"} 3
`

func TestBuildFromDCGMExporter49(t *testing.T) {
	samples, err := promtext.Parse(strings.NewReader(dcgm49))
	if err != nil {
		t.Fatal(err)
	}
	rows, unknown := Build(time.Unix(1_700_000_000, 0), samples, Options{})
	if len(rows) != 1 || len(unknown) != 0 {
		t.Fatalf("rows=%d unknown=%v", len(rows), unknown)
	}
	r := rows[0]
	if r.Identity["host"] != "node1" || r.Identity["driver_version"] != "570.1" || !r.DCGM47 {
		t.Fatalf("identity not read from the 4.9 labels: %+v dcgm47=%v", r.Identity, r.DCGM47)
	}
	want := map[string]float64{
		"gpu_util_pct": 97, "fb_used_mib": 40960, "energy_mj": 1.5e6, "sm_clock_mhz": 1980,
		"max_sm_clock_mhz": 1980, "power_w": 612.5, "power_violation_us": 12.5e6,
		"tensor_active": 0.41, "xid": 0, "ecc_sbe_volatile": 3,
	}
	for col, v := range want {
		if got, ok := r.Values[col]; !ok || math.Abs(got-v) > 1e-6*math.Max(1, v) {
			t.Errorf("%s = %v (present %v), want %v", col, got, ok, v)
		}
	}
}

func TestReasonDeriver(t *testing.T) {
	row := func(sec int64, power, thermal float64, v2 bool) []Row {
		return []Row{{TS: time.Unix(sec, 0), Identity: map[string]string{"host": "h", "gpu_index": "0", "gpu_uuid": "u"},
			Values: map[string]float64{"power_violation_us": power, "thermal_violation_us": thermal}, DCGM47: v2}}
	}
	var d ReasonDeriver
	a := row(0, 0, 0, true)
	d.Apply(a)
	if _, ok := a[0].Values["clock_event_reasons"]; ok {
		t.Error("the first scrape has nothing to compare with: the column must stay empty")
	}
	b := row(10, 8e6, 1e6, true) // 8 of 10 s power-throttled, 1 of 10 s thermal
	d.Apply(b)
	if got := uint64(b[0].Values["clock_event_reasons"]); got != fields.ReasonSWPowerCap {
		t.Errorf("reasons = %#x, want the power-cap bit only", got)
	}
	c := row(20, 8e6, 1e6, true) // no growth: not throttled
	d.Apply(c)
	if got, ok := c[0].Values["clock_event_reasons"]; !ok || got != 0 {
		t.Errorf("reasons = %v (present %v), want 0", got, ok)
	}
	e := row(30, 1e6, 9e6, true) // power counter restarted; thermal throttled 8 of 10 s
	d.Apply(e)
	if got := uint64(e[0].Values["clock_event_reasons"]); got != fields.ReasonSWThermalSlowdown {
		t.Errorf("reasons = %#x, want the thermal bit only", got)
	}
	// Classic names: the counters' unit differs between DCGM versions, so nothing is derived.
	var d2 ReasonDeriver
	d2.Apply(row(0, 0, 0, false))
	f := row(10, 9e6, 0, false)
	d2.Apply(f)
	if _, ok := f[0].Values["clock_event_reasons"]; ok || d2.Derived != 0 {
		t.Error("classic-name counters must not be used to derive reasons")
	}
	// A GPU that reports the bitmask keeps it.
	g := row(40, 20e6, 9e6, true)
	g[0].Values["clock_event_reasons"] = 0x2
	d.Apply(g)
	if g[0].Values["clock_event_reasons"] != 0x2 {
		t.Error("a served bitmask was overwritten")
	}
}

// dcgm-exporter 4.8.x and earlier serve the violation counters in nanoseconds;
// the column is in microseconds.
func TestBuildConvertsClassicViolationCounters(t *testing.T) {
	const classic = `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-1",modelName="NVIDIA A100-SXM4-40GB",Hostname="node1"} 97
DCGM_FI_DEV_POWER_VIOLATION{gpu="0",UUID="GPU-1",modelName="NVIDIA A100-SXM4-40GB",Hostname="node1"} 2.5e+12
DCGM_FI_DEV_THERMAL_VIOLATION{gpu="0",UUID="GPU-1",modelName="NVIDIA A100-SXM4-40GB",Hostname="node1"} 0
`
	samples, err := promtext.Parse(strings.NewReader(classic))
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

// dcgm-exporter serves DCGM_EXP_XID_ERRORS_COUNT as one series per XID code in
// its time window, or one unlabeled 0 when there were none.
func TestXIDCount(t *testing.T) {
	const id = `gpu="0",UUID="GPU-a",modelName="NVIDIA A100-SXM4-40GB",hostname="n1",window_size_in_ms="300000"`
	build := func(text string) Row {
		t.Helper()
		ss, err := promtext.Parse(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		rows, unknown := Build(time.Unix(1_700_000_000, 0), ss, Options{})
		if len(rows) != 1 || len(unknown) != 0 {
			t.Fatalf("want 1 row and no unknown metrics, got %d rows, unknown %v", len(rows), unknown)
		}
		return rows[0]
	}
	quiet := build("DCGM_EXP_XID_ERRORS_COUNT{" + id + "} 0\n")
	if v, ok := quiet.Values["xid_count"]; !ok || v != 0 {
		t.Errorf("no XIDs: xid_count = %v (present %v), want 0", v, ok)
	}
	if _, ok := quiet.Values["xid"]; ok {
		t.Error("no XIDs: the xid column must stay empty")
	}
	two := build("DCGM_EXP_XID_ERRORS_COUNT{" + id + `,xid="79"} 1` + "\nDCGM_EXP_XID_ERRORS_COUNT{" + id + `,xid="31"} 3` + "\n")
	if two.Values["xid_count"] != 4 || two.Values["xid"] != 31 {
		t.Errorf("two codes: xid_count %v xid %v, want the total 4 and the most frequent code 31", two.Values["xid_count"], two.Values["xid"])
	}
	// The last-XID field, when DCGM serves it, is what the xid column holds.
	both := build("DCGM_EXP_XID_ERRORS_COUNT{" + id + `,xid="31"} 3` + "\nDCGM_FI_DEV_XID_ERROR{" + id + "} 79\n")
	if both.Values["xid_count"] != 3 || both.Values["xid"] != 79 {
		t.Errorf("with the last-XID field: xid_count %v xid %v, want 3 and 79", both.Values["xid_count"], both.Values["xid"])
	}
}
