// Package fields is the single source of truth for which DCGM fields InfraPulse
// collects, how each maps to a column in the canonical gpu_samples schema, and
// which data tier it belongs to.
//
// Tiers follow the InfraPulse data contract:
//
//	A  activity        (utilization, memory, power, energy, temperature)
//	B  clocks & health (clocks, HBM temperature, throttle reasons, violations, XID, ECC)
//	C  profiling       (SM / tensor / DRAM activity, PCIe and NVLink bytes)
//	S  static          (limits and maximum clocks; read once, repeated per sample)
package fields

import "sort"

// Tier identifies which part of the data contract a field belongs to.
type Tier string

const (
	TierA Tier = "A"
	TierB Tier = "B"
	TierC Tier = "C"
	TierS Tier = "S"
)

// Kind mirrors the Prometheus metric type dcgm-exporter uses.
type Kind string

const (
	Gauge   Kind = "gauge"
	Counter Kind = "counter"
)

// Field describes one DCGM field.
type Field struct {
	ID      int      // DCGM field ID
	Name    string   // DCGM / dcgm-exporter metric name
	Aliases []string // older or newer names for the same field
	Column  string   // column in the canonical gpu_samples schema
	Tier    Tier
	Kind    Kind
	Unit    string
	Help    string
	// Scale converts a value read under Name (or an alias) to Unit; 0 means 1.
	// dcgm-exporter serves the violation counters in nanoseconds (DCGM's
	// dcgm_fields.h; a throttled GPU adds 1e9 per second), and the columns are
	// in microseconds. NameHelp is the HELP text for the Name line where the
	// served unit differs from Unit.
	Scale    float64
	NameHelp string
	// EventOnly fields have no value until the event first happens (DCGM reports
	// the last XID only after one occurs), so absence means "none so far".
	EventOnly bool
}

// Catalog lists every field InfraPulse asks for, in canonical column order.
var Catalog = []Field{
	// Tier A: activity
	{ID: 203, Name: "DCGM_FI_DEV_GPU_UTIL", Column: "gpu_util_pct", Tier: TierA, Kind: Gauge, Unit: "%", Help: "GPU utilization: share of time one or more kernels was running (in %)."},
	{ID: 204, Name: "DCGM_FI_DEV_MEM_COPY_UTIL", Column: "mem_copy_util_pct", Tier: TierA, Kind: Gauge, Unit: "%", Help: "Memory utilization (in %)."},
	{ID: 252, Name: "DCGM_FI_DEV_FB_USED", Column: "fb_used_mib", Tier: TierA, Kind: Gauge, Unit: "MiB", Help: "Framebuffer memory used (in MiB)."},
	{ID: 251, Name: "DCGM_FI_DEV_FB_FREE", Column: "fb_free_mib", Tier: TierA, Kind: Gauge, Unit: "MiB", Help: "Framebuffer memory free (in MiB)."},
	{ID: 155, Name: "DCGM_FI_DEV_POWER_USAGE", Column: "power_w", Tier: TierA, Kind: Gauge, Unit: "W", Help: "Power draw (in W)."},
	{ID: 156, Name: "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", Column: "energy_mj", Tier: TierA, Kind: Counter, Unit: "mJ", Help: "Total energy consumption since boot (in mJ)."},
	{ID: 150, Name: "DCGM_FI_DEV_GPU_TEMP", Column: "gpu_temp_c", Tier: TierA, Kind: Gauge, Unit: "C", Help: "GPU temperature (in C)."},

	// Tier B: clocks and health
	{ID: 100, Name: "DCGM_FI_DEV_SM_CLOCK", Column: "sm_clock_mhz", Tier: TierB, Kind: Gauge, Unit: "MHz", Help: "SM clock frequency (in MHz)."},
	{ID: 101, Name: "DCGM_FI_DEV_MEM_CLOCK", Column: "mem_clock_mhz", Tier: TierB, Kind: Gauge, Unit: "MHz", Help: "Memory clock frequency (in MHz)."},
	{ID: 140, Name: "DCGM_FI_DEV_MEMORY_TEMP", Column: "mem_temp_c", Tier: TierB, Kind: Gauge, Unit: "C", Help: "Memory (HBM) temperature (in C)."},
	{ID: 190, Name: "DCGM_FI_DEV_PSTATE", Column: "pstate", Tier: TierB, Kind: Gauge, Unit: "", Help: "Performance state (P-state) 0-15."},
	{ID: 112, Name: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS", Aliases: []string{"DCGM_FI_DEV_CLOCK_THROTTLE_REASONS"}, Column: "clock_event_reasons", Tier: TierB, Kind: Gauge, Unit: "bitmask", Help: "Current clock event (throttle) reasons bitmask."},
	{ID: 240, Name: "DCGM_FI_DEV_POWER_VIOLATION", Column: "power_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to power constraints (in us; dcgm-exporter serves ns).", Scale: 1e-3, NameHelp: "Throttling duration due to power constraints (in ns)."},
	{ID: 241, Name: "DCGM_FI_DEV_THERMAL_VIOLATION", Column: "thermal_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to thermal constraints (in us; dcgm-exporter serves ns).", Scale: 1e-3, NameHelp: "Throttling duration due to thermal constraints (in ns)."},
	{ID: 242, Name: "DCGM_FI_DEV_SYNC_BOOST_VIOLATION", Column: "sync_boost_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to sync-boost constraints (in us; dcgm-exporter serves ns).", Scale: 1e-3, NameHelp: "Throttling duration due to sync-boost constraints (in ns)."},
	{ID: 243, Name: "DCGM_FI_DEV_BOARD_LIMIT_VIOLATION", Column: "board_limit_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to board limit constraints (in us; dcgm-exporter serves ns).", Scale: 1e-3, NameHelp: "Throttling duration due to board limit constraints (in ns)."},
	{ID: 245, Name: "DCGM_FI_DEV_RELIABILITY_VIOLATION", Column: "reliability_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to reliability constraints (in us; dcgm-exporter serves ns).", Scale: 1e-3, NameHelp: "Throttling duration due to reliability constraints (in ns)."},
	{ID: 230, Name: "DCGM_FI_DEV_XID_ERRORS", Column: "xid", Tier: TierB, Kind: Gauge, Unit: "code", Help: "Value of the last XID error encountered.", EventOnly: true},
	{ID: 310, Name: "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", Column: "ecc_sbe_volatile", Tier: TierB, Kind: Counter, Unit: "count", Help: "Total single-bit (correctable) volatile ECC errors."},
	{ID: 311, Name: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", Column: "ecc_dbe_volatile", Tier: TierB, Kind: Counter, Unit: "count", Help: "Total double-bit (uncorrectable) volatile ECC errors."},
	{ID: 394, Name: "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", Column: "remapped_rows_correctable", Tier: TierB, Kind: Counter, Unit: "count", Help: "Rows remapped for correctable errors."},
	{ID: 393, Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Column: "remapped_rows_uncorrectable", Tier: TierB, Kind: Counter, Unit: "count", Help: "Rows remapped for uncorrectable errors."},
	{ID: 395, Name: "DCGM_FI_DEV_ROW_REMAP_FAILURE", Column: "row_remap_failure", Tier: TierB, Kind: Gauge, Unit: "bool", Help: "Whether remapping of rows has failed."},
	{ID: 202, Name: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", Column: "pcie_replay_count", Tier: TierB, Kind: Counter, Unit: "count", Help: "Total PCIe retries."},

	// Tier C: profiling (ratios 0..1 as reported by dcgm-exporter)
	{ID: 1001, Name: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", Column: "gr_engine_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of time the graphics engine is active."},
	{ID: 1002, Name: "DCGM_FI_PROF_SM_ACTIVE", Column: "sm_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles an SM has at least 1 warp assigned."},
	{ID: 1003, Name: "DCGM_FI_PROF_SM_OCCUPANCY", Column: "sm_occupancy", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of resident warps to the maximum."},
	{ID: 1004, Name: "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", Column: "tensor_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the tensor (HMMA) pipe is active."},
	{ID: 1005, Name: "DCGM_FI_PROF_DRAM_ACTIVE", Column: "dram_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the device memory interface is active."},
	{ID: 1006, Name: "DCGM_FI_PROF_PIPE_FP64_ACTIVE", Column: "fp64_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the FP64 pipe is active."},
	{ID: 1007, Name: "DCGM_FI_PROF_PIPE_FP32_ACTIVE", Column: "fp32_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the FP32 pipe is active."},
	{ID: 1008, Name: "DCGM_FI_PROF_PIPE_FP16_ACTIVE", Column: "fp16_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the FP16 pipe is active."},
	{ID: 1009, Name: "DCGM_FI_PROF_PCIE_TX_BYTES", Column: "pcie_tx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "PCIe bytes transmitted per second."},
	{ID: 1010, Name: "DCGM_FI_PROF_PCIE_RX_BYTES", Column: "pcie_rx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "PCIe bytes received per second."},
	{ID: 1011, Name: "DCGM_FI_PROF_NVLINK_TX_BYTES", Column: "nvlink_tx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "NVLink bytes transmitted per second."},
	{ID: 1012, Name: "DCGM_FI_PROF_NVLINK_RX_BYTES", Column: "nvlink_rx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "NVLink bytes received per second."},

	// Static device facts
	{ID: 164, Name: "DCGM_FI_DEV_ENFORCED_POWER_LIMIT", Column: "enforced_power_limit_w", Tier: TierS, Kind: Gauge, Unit: "W", Help: "Effective power limit the driver enforces (in W)."},
	{ID: 113, Name: "DCGM_FI_DEV_MAX_SM_CLOCK", Column: "max_sm_clock_mhz", Tier: TierS, Kind: Gauge, Unit: "MHz", Help: "Maximum SM clock (in MHz)."},
	{ID: 114, Name: "DCGM_FI_DEV_MAX_MEM_CLOCK", Column: "max_mem_clock_mhz", Tier: TierS, Kind: Gauge, Unit: "MHz", Help: "Maximum memory clock (in MHz)."},
}

// IdentityColumns precede the field columns in every gpu_samples row.
var IdentityColumns = []string{
	"ts", "host", "gpu_index", "gpu_uuid", "model", "driver_version",
	"namespace", "pod", "container", "job_id",
}

var byName map[string]Field

func init() {
	byName = make(map[string]Field, len(Catalog)*2)
	for _, f := range Catalog {
		byName[f.Name] = f
		for _, a := range f.Aliases {
			byName[a] = f
		}
	}
}

// NameScale is the factor that converts a value served under Name to Unit.
func (f Field) NameScale() float64 {
	if f.Scale == 0 {
		return 1
	}
	return f.Scale
}

// ExporterHelp is the HELP text for the field's line in a dcgm-exporter field
// file: it describes the value in the unit the exporter serves.
func (f Field) ExporterHelp() string {
	if f.NameHelp != "" {
		return f.NameHelp
	}
	return f.Help
}

// Lookup returns the catalog entry for a metric name or alias.
func Lookup(metric string) (Field, bool) {
	f, ok := byName[metric]
	return f, ok
}

// Columns returns the full canonical column order: identity columns, then fields.
func Columns() []string {
	cols := append([]string{}, IdentityColumns...)
	for _, f := range Catalog {
		cols = append(cols, f.Column)
	}
	return cols
}

// ByTier groups the catalog by tier, in a stable order.
func ByTier() map[Tier][]Field {
	out := map[Tier][]Field{}
	for _, f := range Catalog {
		out[f.Tier] = append(out[f.Tier], f)
	}
	for t := range out {
		sort.SliceStable(out[t], func(i, j int) bool { return out[t][i].ID < out[t][j].ID })
	}
	return out
}

// Throttle (clock event) reason bits, as defined by NVML.
const (
	ReasonGPUIdle             uint64 = 0x1
	ReasonAppClocksSetting    uint64 = 0x2
	ReasonSWPowerCap          uint64 = 0x4
	ReasonHWSlowdown          uint64 = 0x8
	ReasonSyncBoost           uint64 = 0x10
	ReasonSWThermalSlowdown   uint64 = 0x20
	ReasonHWThermalSlowdown   uint64 = 0x40
	ReasonHWPowerBrake        uint64 = 0x80
	ReasonDisplayClockSetting uint64 = 0x100
)

// ReasonNames maps each bit to a readable name.
var ReasonNames = map[uint64]string{
	ReasonGPUIdle:             "gpu_idle",
	ReasonAppClocksSetting:    "applications_clocks_setting",
	ReasonSWPowerCap:          "sw_power_cap",
	ReasonHWSlowdown:          "hw_slowdown",
	ReasonSyncBoost:           "sync_boost",
	ReasonSWThermalSlowdown:   "sw_thermal_slowdown",
	ReasonHWThermalSlowdown:   "hw_thermal_slowdown",
	ReasonHWPowerBrake:        "hw_power_brake_slowdown",
	ReasonDisplayClockSetting: "display_clock_setting",
}
