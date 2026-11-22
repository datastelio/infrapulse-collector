package fields

import "strings"

// AMD Instinct GPUs are read from the AMD Device Metrics Exporter
// (ROCm/device-metrics-exporter), which serves Prometheus text like
// dcgm-exporter but with its own metric and label names. Each metric maps onto
// a column of the same canonical gpu_samples schema, so everything downstream
// (the InfraPulse analyzer and platform) reads AMD and NVIDIA data alike.
//
// Names follow the exporter's metrics list (exporter v1.3+, MI2xx/MI3xx). The
// exporter writes them in lower case; lookups ignore case.

// AMDField maps one exporter metric (optionally one value of one label) onto a
// canonical column.
type AMDField struct {
	Name string
	// LabelKey/LabelValue select one series of a labelled metric, for example
	// gpu_clock{clock_type="system"}. LabelValue matches case-insensitively and
	// also matches the enum spelling (GPU_CLOCK_TYPE_SYSTEM).
	LabelKey, LabelValue string
	Column               string
	// Scale converts to the column's unit (percent -> ratio, uJ -> mJ).
	Scale float64
	// Priority orders several metrics feeding one column: the lowest number
	// reported for a GPU wins (gpu_power_usage before gpu_package_power).
	Priority int
	Help     string
}

// AMD-only inputs that are not canonical columns: the exporter's throttle
// residency percentages, turned into the NVML-style clock_event_reasons bitmask
// (see AMDReasons).
const (
	AMDPowerViolationPct   = "_amd_ppt_violation_pct"
	AMDThermalViolationPct = "_amd_thermal_violation_pct"
	AMDHBMViolationPct     = "_amd_hbm_thermal_violation_pct"
)

// AMDCatalog lists every exporter metric InfraPulse reads.
var AMDCatalog = []AMDField{
	// Tier A
	{Name: "gpu_gfx_activity", Column: "gpu_util_pct", Scale: 1, Help: "Graphics/compute engine activity (%), the counterpart of DCGM GPU_UTIL."},
	{Name: "gpu_umc_activity", Column: "mem_copy_util_pct", Scale: 1, Help: "Memory controller (UMC) activity (%)."},
	{Name: "gpu_umc_activity", Column: "dram_active", Scale: 0.01, Priority: 1, Help: "Memory controller activity as a ratio: the device-memory activity used for delivered work."},
	{Name: "gpu_used_vram", Column: "fb_used_mib", Scale: 1, Help: "VRAM used (MB)."},
	{Name: "gpu_free_vram", Column: "fb_free_mib", Scale: 1, Help: "VRAM free (MB)."},
	{Name: "gpu_power_usage", Column: "power_w", Scale: 1, Help: "Power draw (W)."},
	{Name: "gpu_package_power", Column: "power_w", Scale: 1, Priority: 1, Help: "Socket power (W), MI3xx."},
	{Name: "gpu_average_package_power", Column: "power_w", Scale: 1, Priority: 2, Help: "Average socket power (W), MI2xx."},
	{Name: "gpu_energy_consumed", Column: "energy_mj", Scale: 0.001, Help: "Energy since boot (uJ, stored as mJ)."},
	{Name: "gpu_junction_temperature", Column: "gpu_temp_c", Scale: 1, Help: "Junction (hotspot) temperature (C), MI3xx."},
	{Name: "gpu_edge_temperature", Column: "gpu_temp_c", Scale: 1, Priority: 1, Help: "Edge temperature (C), MI2xx."},

	// Tier B
	{Name: "gpu_clock", LabelKey: "clock_type", LabelValue: "system", Column: "sm_clock_mhz", Scale: 1, Help: "GFX (system) clock (MHz); per-XCD series are averaged."},
	{Name: "gpu_clock", LabelKey: "clock_type", LabelValue: "memory", Column: "mem_clock_mhz", Scale: 1, Help: "Memory clock (MHz)."},
	{Name: "gpu_memory_temperature", Column: "mem_temp_c", Scale: 1, Help: "HBM temperature (C)."},
	{Name: "gpu_hbm_temperature", Column: "mem_temp_c", Scale: 1, Priority: 1, Help: "HBM temperature (C), MI2xx."},
	{Name: "gpu_ecc_correct_total", Column: "ecc_sbe_volatile", Scale: 1, Help: "Correctable ECC errors."},
	{Name: "gpu_ecc_uncorrect_total", Column: "ecc_dbe_volatile", Scale: 1, Help: "Uncorrectable ECC errors."},
	{Name: "pcie_replay_count", Column: "pcie_replay_count", Scale: 1, Help: "PCIe replays."},
	{Name: "gpu_violation_ppt_residency_percentage", Column: AMDPowerViolationPct, Scale: 1, Help: "Time held at the package power limit (%)."},
	{Name: "gpu_violation_socket_thermal_residency_percentage", Column: AMDThermalViolationPct, Scale: 1, Help: "Time throttled by socket temperature (%)."},
	{Name: "gpu_violation_vr_thermal_residency_percentage", Column: AMDThermalViolationPct, Scale: 1, Priority: 1, Help: "Time throttled by voltage-regulator temperature (%)."},
	{Name: "gpu_violation_processor_hot_residency_percentage", Column: AMDThermalViolationPct, Scale: 1, Priority: 2, Help: "Time with PROCHOT asserted (%)."},
	{Name: "gpu_violation_hbm_thermal_residency_percentage", Column: AMDHBMViolationPct, Scale: 1, Help: "Time throttled by HBM temperature (%)."},

	// Tier C (profiler metrics; off by default in the exporter)
	{Name: "gpu_prof_sm_active", Column: "sm_active", Scale: 0.01, Help: "Share of time vector ALU instructions are processed (%)."},
	{Name: "gpu_prof_occupancy_percent", Column: "sm_occupancy", Scale: 0.01, Help: "Occupancy as a share of maximum (%)."},
	{Name: "gpu_prof_tensor_active_percent", Column: "tensor_active", Scale: 0.01, Help: "Matrix core (MFMA) utilization (%)."},
	{Name: "gpu_prof_gui_util_percent", Column: "gr_engine_active", Scale: 0.01, Help: "Share of time the graphics pipeline is active (%)."},

	// Static
	{Name: "gpu_max_clock", LabelKey: "clock_type", LabelValue: "system", Column: "max_sm_clock_mhz", Scale: 1, Help: "Maximum GFX clock (MHz)."},
	{Name: "gpu_max_clock", LabelKey: "clock_type", LabelValue: "memory", Column: "max_mem_clock_mhz", Scale: 1, Help: "Maximum memory clock (MHz)."},
}

var amdByName map[string][]AMDField

func init() {
	amdByName = map[string][]AMDField{}
	for _, f := range AMDCatalog {
		amdByName[f.Name] = append(amdByName[f.Name], f)
	}
}

// LookupAMD returns the catalog entries a sample feeds. A metric can feed more
// than one column (gpu_umc_activity) or none for this label value.
func LookupAMD(metric string, labels map[string]string) []AMDField {
	cands := amdByName[strings.ToLower(metric)]
	var out []AMDField
	for _, f := range cands {
		if f.LabelKey == "" || labelMatches(labels[f.LabelKey], f.LabelValue) {
			out = append(out, f)
		}
	}
	return out
}

// IsAMDMetric reports whether a metric name belongs to the AMD exporter's
// catalog, whatever its labels.
func IsAMDMetric(metric string) bool {
	_, ok := amdByName[strings.ToLower(metric)]
	return ok
}

func labelMatches(got, want string) bool {
	g := strings.ToLower(got)
	return g == want || strings.HasSuffix(g, "_"+want) // GPU_CLOCK_TYPE_SYSTEM
}

// AMDThrottleShare is the residency share at or above which a sample counts as
// throttled for that reason. The exporter reports the share of the sampling
// interval spent at a limit; NVML's bits are instantaneous, so a sample is
// flagged when the limit held for most of its interval.
const AMDThrottleShare = 50.0

// AMDReasons turns the exporter's violation residency percentages into the
// clock_event_reasons bitmask the analyzer reads. ok is false when the GPU
// reported none of them (virtualised GPUs: the exporter serves violations on
// bare metal only), so the column stays empty rather than "no throttling".
func AMDReasons(ppt, thermal, hbm float64, havePPT, haveThermal, haveHBM bool) (mask uint64, ok bool) {
	if !havePPT && !haveThermal && !haveHBM {
		return 0, false
	}
	if havePPT && ppt >= AMDThrottleShare {
		mask |= ReasonSWPowerCap
	}
	if haveThermal && thermal >= AMDThrottleShare {
		mask |= ReasonSWThermalSlowdown
	}
	if haveHBM && hbm >= AMDThrottleShare {
		mask |= ReasonHWThermalSlowdown
	}
	return mask, true
}

// Vendor names the GPU vendor from a model name ("AMD Instinct MI300X",
// "NVIDIA H100 80GB HBM3"); "" when it cannot tell.
func Vendor(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "amd") || strings.Contains(m, "instinct") || strings.Contains(m, "radeon") || isMIName(strings.TrimSpace(m)):
		return "amd"
	case strings.Contains(m, "nvidia") || strings.Contains(m, "tesla") || strings.Contains(m, "geforce"):
		return "nvidia"
	}
	return ""
}

// isMIName matches bare Instinct names such as "MI300X".
func isMIName(m string) bool {
	return len(m) > 2 && strings.HasPrefix(m, "mi") && m[2] >= '0' && m[2] <= '9'
}

// amdDeviceIDs names Instinct GPUs whose exporter reports card_model as a PCI
// device ID rather than a product name.
var amdDeviceIDs = map[string]string{
	"0x74a1": "MI300X", "0x74a5": "MI325X", "0x74a0": "MI300A",
	"0x740c": "MI250X", "0x740f": "MI210",
}

// AMDModelName turns the exporter's card_model into a model name the analyzer
// matches against its spec sheets and the vendor test: "AMD Instinct MI300X".
func AMDModelName(card string) string {
	c := strings.TrimSpace(card)
	if c == "" {
		return ""
	}
	if n, ok := amdDeviceIDs[strings.ToLower(c)]; ok {
		return "AMD Instinct " + n
	}
	if Vendor(c) == "amd" && !isMIName(strings.ToLower(c)) {
		return c
	}
	return "AMD " + c
}

// AMDNamesFor lists the exporter metrics that feed a canonical column (for
// ipcollect check); clock_event_reasons comes from the violation residencies.
func AMDNamesFor(column string) []string {
	if column == "clock_event_reasons" {
		return []string{"gpu_violation_{ppt,socket_thermal,vr_thermal,processor_hot,hbm_thermal}_residency_percentage (derived)"}
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range AMDCatalog {
		name := f.Name
		if f.LabelKey != "" {
			name += "{" + f.LabelKey + "=" + f.LabelValue + "}"
		}
		if f.Column == column && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
