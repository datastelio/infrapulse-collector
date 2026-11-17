package sample

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNum(t *testing.T) {
	cases := map[string]string{
		"45": "45", " 250.123 ": "250.12", "400.00": "400", "(1500Mhz)": "1500",
		"[N/A]": "", "[Not Supported]": "", "N/A": "", "": "", "abc": "",
	}
	for in, want := range cases {
		if got := Num(in); got != want {
			t.Errorf("Num(%q) = %q, want %q", in, got, want)
		}
	}
}

func testSample() Sample {
	return Sample{
		Time: time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC), Vendor: "nvidia", Index: 1,
		Model: `Test "GPU", 80GB`, ID: "abc123", UtilGPU: "97", Power: "300.5", Throttle: "none",
	}
}

func TestRoundTrip(t *testing.T) {
	for _, format := range []string{"csv", "jsonl"} {
		path := filepath.Join(t.TempDir(), "out."+format)
		for range 2 { // second open appends without a second header
			w, err := Create(path, format)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Write(testSample()); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s: got %d rows, want 2", format, len(rows))
		}
		r := rows[1]
		if r["timestamp"] != "2026-11-17T08:00:00Z" || r["gpu_index"] != "1" || r["gpu_model"] != `Test "GPU", 80GB` ||
			r["util_gpu_pct"] != "97" || r["power_w"] != "300.5" || r["temp_c"] != "" || r["throttle_reasons"] != "none" {
			t.Errorf("%s: row = %v", format, r)
		}
		if len(r) != len(Columns) {
			t.Errorf("%s: %d columns, want %d", format, len(r), len(Columns))
		}
	}
}

func TestJSONLNullsAndPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	w, _ := Create(path, "jsonl")
	w.Write(testSample())
	w.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"temp_c":null`) || !strings.Contains(string(data), `"util_gpu_pct":97`) {
		t.Errorf("jsonl = %s", data)
	}
	os.WriteFile(path, append(data, []byte(`{"timestamp":"2026-`)...), 0o644)
	rows, err := ReadFile(path)
	if err != nil || len(rows) != 1 {
		t.Errorf("got %d rows, err %v; want the partial line skipped", len(rows), err)
	}
}

func TestCreateRejectsFormat(t *testing.T) {
	if _, err := Create(filepath.Join(t.TempDir(), "x"), "xml"); err == nil {
		t.Error("want error")
	}
}
