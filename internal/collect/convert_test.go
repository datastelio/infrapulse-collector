package collect

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGz writes text to a gzip file.
func writeGz(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// apiMatrix renders one series as a Prometheus API matrix result.
func apiMatrix(name, labels string, points ...[2]float64) string {
	var vals []string
	for _, p := range points {
		vals = append(vals, fmt.Sprintf(`[%.3f,"%g"]`, p[0], p[1]))
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"%s",%s},"values":[%s]}]}}`,
		name, labels, strings.Join(vals, ","))
}

func readCSV(t *testing.T, dir, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, r := range recs[1:] {
		m := map[string]string{}
		for i, c := range recs[0] {
			m[c] = r[i]
		}
		out = append(out, m)
	}
	return out
}

// An export from Prometheus of a dcgm-exporter 4.9 node in Kubernetes: DCGM 4.7
// names and units, the workload's pod renamed to exported_pod, hourly files
// that share the sample on their boundary.
func TestConvertPrometheusExport(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	const lab = `"gpu":"0","UUID":"GPU-a","modelName":"NVIDIA A100-SXM4-40GB","hostname":"n1","DCGM_FI_SYSTEM_DRIVER_VERSION":"580.105.08","instance":"10.0.0.7:9400","job":"dcgm","pod":"dcgm-exporter-x","namespace":"gpu-operator","exported_pod":"train-0","exported_namespace":"ml"`
	const t0 = 1_795_414_866.534
	writeGz(t, filepath.Join(in, "DCGM_FI_DEV_GPU_UTIL_RATIO_h000.json.gz"), apiMatrix("DCGM_FI_DEV_GPU_UTIL_RATIO", lab, [2]float64{t0, 0.97}, [2]float64{t0 + 10, 1}))
	writeGz(t, filepath.Join(in, "DCGM_FI_DEV_GPU_UTIL_RATIO_h001.json.gz"), apiMatrix("DCGM_FI_DEV_GPU_UTIL_RATIO", lab, [2]float64{t0 + 10, 1}, [2]float64{t0 + 20, 1}))
	writeGz(t, filepath.Join(in, "DCGM_FI_DEV_SM_CLOCK_HERTZ_h000.json.gz"), apiMatrix("DCGM_FI_DEV_SM_CLOCK_HERTZ", lab, [2]float64{t0, 1.41e9}, [2]float64{t0 + 10, 6.3e8}))
	writeGz(t, filepath.Join(in, "DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL_h000.json.gz"), apiMatrix("DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL", lab, [2]float64{t0, 0}, [2]float64{t0 + 10, 8.077822}))
	writeGz(t, filepath.Join(in, "DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL_h001.json.gz"), apiMatrix("DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL", lab, [2]float64{t0 + 20, 8.077822}))
	writeGz(t, filepath.Join(in, "DCGM_EXP_XID_ERRORS_COUNT_h000.json.gz"), apiMatrix("DCGM_EXP_XID_ERRORS_COUNT", lab+`,"window_size_in_ms":"300000"`, [2]float64{t0, 0}, [2]float64{t0 + 10, 0}))
	if err := os.Mkdir(filepath.Join(in, "another-export"), 0o755); err != nil { // not read
		t.Fatal(err)
	}
	writeGz(t, filepath.Join(in, "another-export", "DCGM_FI_DEV_GPU_UTIL_RATIO.json.gz"), apiMatrix("DCGM_FI_DEV_GPU_UTIL_RATIO", lab, [2]float64{t0 + 5, 0.5}))

	meta, err := Convert(ConvertConfig{Inputs: []string{in}, OutDir: out, Format: "csv"})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Rows != 3 || meta.GPUs != 1 || !meta.DCGM47 || !meta.ReasonsDerived || meta.IntervalSeconds != 10 {
		t.Fatalf("meta = %+v", meta)
	}
	rows := readCSV(t, out, meta.SamplesFile)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (the boundary sample once), got %d", len(rows))
	}
	r := rows[1]
	want := map[string]string{"ts": "2026-11-23T06:21:16.534Z", "host": "n1", "gpu_index": "0", "pod": "train-0", "namespace": "ml",
		"driver_version": "580.105.08", "gpu_util_pct": "100", "sm_clock_mhz": "630", "power_violation_us": "8.077822e+06",
		"clock_event_reasons": "4", "xid_count": "0", "xid": ""}
	for c, v := range want {
		if r[c] != v {
			t.Errorf("row 2 %s = %q, want %q", c, r[c], v)
		}
	}
	if rows[0]["clock_event_reasons"] != "" || rows[2]["clock_event_reasons"] != "0" {
		t.Errorf("reasons: first %q (want empty: no earlier scrape), third %q (want 0)", rows[0]["clock_event_reasons"], rows[2]["clock_event_reasons"])
	}

	// The same samples as `promtool tsdb dump` writes them give the same file.
	dump := filepath.Join(t.TempDir(), "dump.txt")
	const dl = `gpu="0", UUID="GPU-a", modelName="NVIDIA A100-SXM4-40GB", hostname="n1", DCGM_FI_SYSTEM_DRIVER_VERSION="580.105.08", instance="10.0.0.7:9400", job="dcgm", pod="dcgm-exporter-x", namespace="gpu-operator", exported_pod="train-0", exported_namespace="ml"`
	text := ""
	for _, s := range []struct {
		name string
		ms   int64
		v    string
	}{
		{"DCGM_FI_DEV_GPU_UTIL_RATIO", 0, "0.97"}, {"DCGM_FI_DEV_GPU_UTIL_RATIO", 10000, "1"}, {"DCGM_FI_DEV_GPU_UTIL_RATIO", 20000, "1"},
		{"DCGM_FI_DEV_SM_CLOCK_HERTZ", 0, "1.41e+09"}, {"DCGM_FI_DEV_SM_CLOCK_HERTZ", 10000, "6.3e+08"},
		{"DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL", 0, "0"}, {"DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL", 10000, "8.077822"},
		{"DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL", 20000, "8.077822"},
		{"DCGM_EXP_XID_ERRORS_COUNT", 0, "0"}, {"DCGM_EXP_XID_ERRORS_COUNT", 10000, "0"},
	} {
		text += fmt.Sprintf("{__name__=\"%s\", %s} %s %d\n", s.name, dl, s.v, int64(1_795_414_866_534)+s.ms)
	}
	if err := os.WriteFile(dump, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	out2 := t.TempDir()
	meta2, err := Convert(ConvertConfig{Inputs: []string{dump}, OutDir: out2, Format: "csv"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(out, meta.SamplesFile))
	b, _ := os.ReadFile(filepath.Join(out2, meta2.SamplesFile))
	if string(a) != string(b) {
		t.Errorf("API export and tsdb dump differ:\n%s\n%s", a, b)
	}
}

func TestConvertRejectsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	if _, err := Convert(ConvertConfig{Inputs: []string{dir}, OutDir: t.TempDir(), Format: "csv"}); err == nil {
		t.Error("an empty folder must be an error")
	}
	bad := filepath.Join(dir, "x.json")
	os.WriteFile(bad, []byte(`{"status":"error","error":"query timed out"}`), 0o644)
	if _, err := Convert(ConvertConfig{Inputs: []string{dir}, OutDir: t.TempDir(), Format: "csv"}); err == nil || !strings.Contains(err.Error(), "x.json") {
		t.Errorf("a failed query result must be an error naming the file, got %v", err)
	}
	// A single scrape of /metrics has no timestamps to make rows from.
	os.WriteFile(bad, []byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up"},"values":[[1795414866,"1"]]}]}}`), 0o644)
	if _, err := Convert(ConvertConfig{Inputs: []string{dir}, OutDir: t.TempDir(), Format: "csv"}); err == nil || !strings.Contains(err.Error(), "no GPU metrics") {
		t.Errorf("an export without GPU metrics must say so, got %v", err)
	}
}
