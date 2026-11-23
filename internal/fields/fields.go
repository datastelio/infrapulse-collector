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
	ID      int      // DCGM field ID; 0 for a metric dcgm-exporter computes itself (DCGM_EXP_*)
	Name    string   // DCGM / dcgm-exporter metric name
	Aliases []string // older or newer names for the same field
	Column  string   // column in the canonical gpu_samples schema
	Tier    Tier
	Kind    Kind
	Unit    string
	Help    string
	// Scale converts a value read under Name (or an alias) to Unit; 0 means 1.
	// dcgm-exporter 4.8.x and earlier serve the violation counters in
	// nanoseconds (DCGM's dcgm_fields.h; a throttled GPU adds 1e9 per second),
	// and the columns are in microseconds. NameHelp is the HELP text for the
	// Name line where the served unit differs from Unit.
	Scale    float64
	NameHelp string
	// EventOnly fields have no value until the event first happens (DCGM reports
	// the last XID only after one occurs), so absence means "none so far".
	EventOnly bool
	// V2 is the field's name in DCGM 4.7, which dcgm-exporter 4.9.0 and later
	// serve; "" when 4.9 does not export it (it rejects bitmask fields). A value
	// read under the V2 name times V2Scale is in this field's Unit: 4.9 reports
	// hertz, bytes, joules, seconds and 0 to 1 ratios where earlier versions
	// reported MHz, MiB, mJ, nanoseconds and percent.
	V2      string
	V2Scale float64
	V2Help  string
	// FileNote is a comment written above the field's line in the dcgm-exporter
	// field files.
	FileNote string
}

// XIDCount is dcgm-exporter's own XID counter, not a DCGM field (so it has no
// field ID): one series per GPU and XID code (label "xid") holding how many of
// that XID DCGM recorded in the exporter's time window (5 minutes unless
// --xid-count-window-size says otherwise), and one unlabeled series reading 0
// for a GPU with none. Unlike the last-XID field it is present from the first
// scrape. The xid_count column is the sum over codes.
const (
	XIDCount       = "DCGM_EXP_XID_ERRORS_COUNT"
	XIDCountColumn = "xid_count"
	XIDCodeLabel   = "xid"
)

// Catalog lists every field InfraPulse asks for, in canonical column order.
var Catalog = []Field{
	// Tier A: activity
	{ID: 203, Name: "DCGM_FI_DEV_GPU_UTIL", Column: "gpu_util_pct", Tier: TierA, Kind: Gauge, Unit: "%", Help: "GPU utilization: share of time one or more kernels was running (in %).", V2: "DCGM_FI_DEV_GPU_UTIL_RATIO", V2Scale: 100, V2Help: "GPU utilization as a ratio."},
	{ID: 204, Name: "DCGM_FI_DEV_MEM_COPY_UTIL", Column: "mem_copy_util_pct", Tier: TierA, Kind: Gauge, Unit: "%", Help: "Memory utilization (in %).", V2: "DCGM_FI_DEV_MEMORY_UTIL_RATIO", V2Scale: 100, V2Help: "Memory utilization as a ratio."},
	{ID: 252, Name: "DCGM_FI_DEV_FB_USED", Column: "fb_used_mib", Tier: TierA, Kind: Gauge, Unit: "MiB", Help: "Framebuffer memory used (in MiB).", V2: "DCGM_FI_DEV_FB_USED_BYTES", V2Scale: 1.0 / (1 << 20), V2Help: "Used framebuffer memory in bytes."},
	{ID: 251, Name: "DCGM_FI_DEV_FB_FREE", Column: "fb_free_mib", Tier: TierA, Kind: Gauge, Unit: "MiB", Help: "Framebuffer memory free (in MiB).", V2: "DCGM_FI_DEV_FB_FREE_BYTES", V2Scale: 1.0 / (1 << 20), V2Help: "Free framebuffer memory in bytes."},
	{ID: 155, Name: "DCGM_FI_DEV_POWER_USAGE", Column: "power_w", Tier: TierA, Kind: Gauge, Unit: "W", Help: "Power draw (in W).", V2: "DCGM_FI_DEV_BOARD_POWER_WATTS", V2Scale: 1, V2Help: "Power draw (in W)."},
	{ID: 156, Name: "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", Column: "energy_mj", Tier: TierA, Kind: Counter, Unit: "mJ", Help: "Total energy consumption since boot (in mJ).", V2: "DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL", V2Scale: 1000, V2Help: "GPU energy consumed since the driver was reloaded in joules."},
	{ID: 150, Name: "DCGM_FI_DEV_GPU_TEMP", Column: "gpu_temp_c", Tier: TierA, Kind: Gauge, Unit: "C", Help: "GPU temperature (in C).", V2: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", V2Scale: 1, V2Help: "GPU temperature (in C)."},

	// Tier B: clocks and health
	{ID: 100, Name: "DCGM_FI_DEV_SM_CLOCK", Column: "sm_clock_mhz", Tier: TierB, Kind: Gauge, Unit: "MHz", Help: "SM clock frequency (in MHz).", V2: "DCGM_FI_DEV_SM_CLOCK_HERTZ", V2Scale: 1e-6, V2Help: "SM clock frequency in hertz."},
	{ID: 101, Name: "DCGM_FI_DEV_MEM_CLOCK", Column: "mem_clock_mhz", Tier: TierB, Kind: Gauge, Unit: "MHz", Help: "Memory clock frequency (in MHz).", V2: "DCGM_FI_DEV_MEMORY_CLOCK_HERTZ", V2Scale: 1e-6, V2Help: "Memory clock frequency in hertz."},
	{ID: 140, Name: "DCGM_FI_DEV_MEMORY_TEMP", Column: "mem_temp_c", Tier: TierB, Kind: Gauge, Unit: "C", Help: "Memory (HBM) temperature (in C).", V2: "DCGM_FI_DEV_MEMORY_TEMP_CELSIUS", V2Scale: 1, V2Help: "Memory temperature (in C)."},
	{ID: 190, Name: "DCGM_FI_DEV_PSTATE", Column: "pstate", Tier: TierB, Kind: Gauge, Unit: "", Help: "Performance state (P-state) 0-15.", V2: "DCGM_FI_DEV_GPU_PSTATE", V2Scale: 1, V2Help: "Performance state from 0 (highest) to 15."},
	{ID: 112, Name: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS", Aliases: []string{"DCGM_FI_DEV_CLOCK_THROTTLE_REASONS"}, Column: "clock_event_reasons", Tier: TierB, Kind: Gauge, Unit: "bitmask", Help: "Current clock event (throttle) reasons bitmask."},
	{ID: 240, Name: "DCGM_FI_DEV_POWER_VIOLATION", Column: "power_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to power constraints (in us; dcgm-exporter 4.8.x and earlier serve ns).", Scale: 1e-3, NameHelp: "Throttling duration due to power constraints (in ns).", V2: "DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL", V2Scale: 1e6, V2Help: "Time below application clocks due to power policy in seconds."},
	{ID: 241, Name: "DCGM_FI_DEV_THERMAL_VIOLATION", Column: "thermal_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to thermal constraints (in us; dcgm-exporter 4.8.x and earlier serve ns).", Scale: 1e-3, NameHelp: "Throttling duration due to thermal constraints (in ns).", V2: "DCGM_FI_DEV_GPU_THERMAL_THROTTLE_SECONDS_TOTAL", V2Scale: 1e6, V2Help: "Time below application clocks due to thermal policy in seconds."},
	{ID: 242, Name: "DCGM_FI_DEV_SYNC_BOOST_VIOLATION", Column: "sync_boost_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to sync-boost constraints (in us; dcgm-exporter 4.8.x and earlier serve ns).", Scale: 1e-3, NameHelp: "Throttling duration due to sync-boost constraints (in ns).", V2: "DCGM_FI_DEV_GPU_SYNC_BOOST_THROTTLE_SECONDS_TOTAL", V2Scale: 1e6, V2Help: "Time below application clocks due to sync boost in seconds."},
	{ID: 243, Name: "DCGM_FI_DEV_BOARD_LIMIT_VIOLATION", Column: "board_limit_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to board limit constraints (in us; dcgm-exporter 4.8.x and earlier serve ns).", Scale: 1e-3, NameHelp: "Throttling duration due to board limit constraints (in ns).", V2: "DCGM_FI_DEV_GPU_LIMIT_THROTTLE_SECONDS_TOTAL", V2Scale: 1e6, V2Help: "Time below application clocks due to board limit in seconds."},
	{ID: 245, Name: "DCGM_FI_DEV_RELIABILITY_VIOLATION", Column: "reliability_violation_us", Tier: TierB, Kind: Counter, Unit: "us", Help: "Throttling duration due to reliability constraints (in us; dcgm-exporter 4.8.x and earlier serve ns).", Scale: 1e-3, NameHelp: "Throttling duration due to reliability constraints (in ns).", V2: "DCGM_FI_DEV_GPU_RELIABILITY_THROTTLE_SECONDS_TOTAL", V2Scale: 1e6, V2Help: "Time below application clocks due to reliability policy in seconds."},
	{ID: 230, Name: "DCGM_FI_DEV_XID_ERRORS", Column: "xid", Tier: TierB, Kind: Gauge, Unit: "code", Help: "Value of the last XID error encountered.", EventOnly: true, V2: "DCGM_FI_DEV_XID_ERROR", V2Scale: 1, V2Help: "Value of the last XID error encountered."},
	{ID: 0, Name: XIDCount, Column: XIDCountColumn, Tier: TierB, Kind: Gauge, Unit: "count", Help: "XID errors in the exporter's time window (5 minutes by default), all codes.", NameHelp: "Count of XID errors within the user-specified time window.", V2: XIDCount, V2Scale: 1, V2Help: "Count of XID errors within the user-specified time window.",
		FileNote: "XID count: the line above holds only the last XID and is absent from /metrics until a GPU has had one.\nThis exporter metric counts each XID code in the exporter's time window (label xid) and reads 0 when there are none."},
	{ID: 310, Name: "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", Column: "ecc_sbe_volatile", Tier: TierB, Kind: Counter, Unit: "count", Help: "Total single-bit (correctable) volatile ECC errors.", V2: "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", V2Scale: 1, V2Help: "Total number of single-bit volatile ECC errors."},
	{ID: 311, Name: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", Column: "ecc_dbe_volatile", Tier: TierB, Kind: Counter, Unit: "count", Help: "Total double-bit (uncorrectable) volatile ECC errors.", V2: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", V2Scale: 1, V2Help: "Total number of double-bit volatile ECC errors."},
	{ID: 394, Name: "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", Column: "remapped_rows_correctable", Tier: TierB, Kind: Counter, Unit: "count", Help: "Rows remapped for correctable errors.", V2: "DCGM_FI_DEV_ROW_REMAP_CORRECTABLE_TOTAL", V2Scale: 1, V2Help: "Number of remapped rows for correctable errors."},
	{ID: 393, Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Column: "remapped_rows_uncorrectable", Tier: TierB, Kind: Counter, Unit: "count", Help: "Rows remapped for uncorrectable errors.", V2: "DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL", V2Scale: 1, V2Help: "Number of remapped rows for uncorrectable errors."},
	{ID: 395, Name: "DCGM_FI_DEV_ROW_REMAP_FAILURE", Column: "row_remap_failure", Tier: TierB, Kind: Gauge, Unit: "bool", Help: "Whether remapping of rows has failed.", V2: "DCGM_FI_DEV_ROW_REMAP_FAILED", V2Scale: 1, V2Help: "Whether remapping of rows has failed."},
	{ID: 202, Name: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", Column: "pcie_replay_count", Tier: TierB, Kind: Counter, Unit: "count", Help: "Total PCIe retries.", V2: "DCGM_FI_DEV_PCIE_REPLAY_TOTAL", V2Scale: 1, V2Help: "Total number of PCIe retries."},

	// Tier C: profiling (ratios 0..1 as reported by dcgm-exporter)
	{ID: 1001, Name: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", Column: "gr_engine_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of time the graphics engine is active.", V2: "DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO", V2Scale: 1, V2Help: "Ratio of time the graphics engine is active."},
	{ID: 1002, Name: "DCGM_FI_PROF_SM_ACTIVE", Column: "sm_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles an SM has at least 1 warp assigned.", V2: "DCGM_FI_PROF_SM_UTIL_RATIO", V2Scale: 1, V2Help: "The ratio of cycles an SM has at least 1 warp assigned."},
	{ID: 1003, Name: "DCGM_FI_PROF_SM_OCCUPANCY", Column: "sm_occupancy", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of resident warps to the maximum.", V2: "DCGM_FI_PROF_SM_OCCUPANCY_RATIO", V2Scale: 1, V2Help: "The ratio of number of warps resident on an SM."},
	{ID: 1004, Name: "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", Column: "tensor_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the tensor (HMMA) pipe is active.", V2: "DCGM_FI_PROF_TENSOR_UTIL_RATIO", V2Scale: 1, V2Help: "Ratio of cycles the tensor (HMMA) pipe is active."},
	{ID: 1005, Name: "DCGM_FI_PROF_DRAM_ACTIVE", Column: "dram_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the device memory interface is active.", V2: "DCGM_FI_PROF_DRAM_UTIL_RATIO", V2Scale: 1, V2Help: "Ratio of cycles the device memory interface is active sending or receiving data."},
	{ID: 1006, Name: "DCGM_FI_PROF_PIPE_FP64_ACTIVE", Column: "fp64_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the FP64 pipe is active.", V2: "DCGM_FI_PROF_FP64_UTIL_RATIO", V2Scale: 1, V2Help: "Ratio of cycles the fp64 pipes are active."},
	{ID: 1007, Name: "DCGM_FI_PROF_PIPE_FP32_ACTIVE", Column: "fp32_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the FP32 pipe is active.", V2: "DCGM_FI_PROF_FP32_UTIL_RATIO", V2Scale: 1, V2Help: "Ratio of cycles the fp32 pipes are active."},
	{ID: 1008, Name: "DCGM_FI_PROF_PIPE_FP16_ACTIVE", Column: "fp16_active", Tier: TierC, Kind: Gauge, Unit: "ratio", Help: "Ratio of cycles the FP16 pipe is active.", V2: "DCGM_FI_PROF_FP16_UTIL_RATIO", V2Scale: 1, V2Help: "Ratio of cycles the fp16 pipes are active."},
	{ID: 1009, Name: "DCGM_FI_PROF_PCIE_TX_BYTES", Column: "pcie_tx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "PCIe bytes transmitted per second.", V2: "DCGM_FI_PROF_PCIE_TX_BYTES", V2Scale: 1, V2Help: "PCIe bytes transmitted per second."},
	{ID: 1010, Name: "DCGM_FI_PROF_PCIE_RX_BYTES", Column: "pcie_rx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "PCIe bytes received per second.", V2: "DCGM_FI_PROF_PCIE_RX_BYTES", V2Scale: 1, V2Help: "PCIe bytes received per second."},
	{ID: 1011, Name: "DCGM_FI_PROF_NVLINK_TX_BYTES", Column: "nvlink_tx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "NVLink bytes transmitted per second.", V2: "DCGM_FI_PROF_NVLINK_TX_BYTES", V2Scale: 1, V2Help: "NVLink bytes transmitted per second.",
		FileNote: "NVLink: add the next two lines only on nodes with NVLink or NVSwitch. They carry no data elsewhere."},
	{ID: 1012, Name: "DCGM_FI_PROF_NVLINK_RX_BYTES", Column: "nvlink_rx_bytes_per_s", Tier: TierC, Kind: Gauge, Unit: "B/s", Help: "NVLink bytes received per second.", V2: "DCGM_FI_PROF_NVLINK_RX_BYTES", V2Scale: 1, V2Help: "NVLink bytes received per second."},

	// Static device facts
	{ID: 164, Name: "DCGM_FI_DEV_ENFORCED_POWER_LIMIT", Column: "enforced_power_limit_w", Tier: TierS, Kind: Gauge, Unit: "W", Help: "Effective power limit the driver enforces (in W).", V2: "DCGM_FI_DEV_BOARD_POWER_LIMIT_ENFORCED_WATTS", V2Scale: 1, V2Help: "Effective board power limit enforced by the driver (in W)."},
	{ID: 113, Name: "DCGM_FI_DEV_MAX_SM_CLOCK", Column: "max_sm_clock_mhz", Tier: TierS, Kind: Gauge, Unit: "MHz", Help: "Maximum SM clock (in MHz).", V2: "DCGM_FI_DEV_SM_MAX_CLOCK_HERTZ", V2Scale: 1e-6, V2Help: "Maximum SM clock in hertz."},
	{ID: 114, Name: "DCGM_FI_DEV_MAX_MEM_CLOCK", Column: "max_mem_clock_mhz", Tier: TierS, Kind: Gauge, Unit: "MHz", Help: "Maximum memory clock (in MHz).", V2: "DCGM_FI_DEV_MEMORY_MAX_CLOCK_HERTZ", V2Scale: 1e-6, V2Help: "Maximum memory clock in hertz."},
}

// IdentityColumns precede the field columns in every gpu_samples row.
var IdentityColumns = []string{
	"ts", "host", "gpu_index", "gpu_uuid", "model", "driver_version",
	"namespace", "pod", "container", "job_id",
}

// Label names dcgm-exporter attaches to every metric. 4.8.3 renamed Hostname to
// hostname, and 4.9.0 renamed the driver-version label.
const (
	LabelHost     = "Hostname"
	LabelHostV2   = "hostname"
	LabelDriver   = "DCGM_FI_DRIVER_VERSION"
	LabelDriverV2 = "DCGM_FI_SYSTEM_DRIVER_VERSION"
)

type entry struct {
	f     Field
	scale float64 // multiply the served value by this to get the catalog unit
	v2    bool    // matched by the DCGM 4.7 name
}

var byName map[string]entry

func init() {
	byName = make(map[string]entry, len(Catalog)*3)
	for _, f := range Catalog {
		// A few fields kept their name in DCGM 4.7 (ECC totals, PCIe and NVLink
		// profiling bytes): the classic entry wins, the unit is the same.
		if f.V2 != "" && f.V2 != f.Name {
			byName[f.V2] = entry{f: f, scale: f.V2Scale, v2: true}
		}
	}
	for _, f := range Catalog {
		byName[f.Name] = entry{f: f, scale: f.NameScale()}
		for _, a := range f.Aliases {
			byName[a] = entry{f: f, scale: f.NameScale()}
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

// ExporterHelp is the HELP text for the field's Name line in a dcgm-exporter
// field file: it describes the value in the unit the exporter serves.
func (f Field) ExporterHelp() string {
	if f.NameHelp != "" {
		return f.NameHelp
	}
	return f.Help
}

// Lookup returns the catalog entry for a metric name, alias or DCGM 4.7 name.
func Lookup(metric string) (Field, bool) {
	e, ok := byName[metric]
	return e.f, ok
}

// LookupScaled is Lookup plus the factor that converts the served value to the
// field's catalog unit, and whether the name is a DCGM 4.7 one (dcgm-exporter
// 4.9.0 and later).
func LookupScaled(metric string) (f Field, scale float64, v2, ok bool) {
	e, ok := byName[metric]
	return e.f, e.scale, e.v2, ok
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
