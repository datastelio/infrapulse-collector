package promtext

import (
	"math"
	"strings"
	"testing"
)

const sample = `# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
DCGM_FI_DEV_SM_CLOCK{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="NVIDIA A100-SXM4-80GB",Hostname="node-1",DCGM_FI_DRIVER_VERSION="550.54.15"} 1410
DCGM_FI_PROF_PIPE_TENSOR_ACTIVE{gpu="0",UUID="GPU-abc",pod="train \"a\"",namespace="ml"} 0.452 1700000000000

DCGM_FI_DEV_XID_ERRORS{gpu="1"} NaN
up 1
`

func TestParse(t *testing.T) {
	got, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 samples, got %d", len(got))
	}
	if got[0].Name != "DCGM_FI_DEV_SM_CLOCK" || got[0].Value != 1410 {
		t.Errorf("first sample wrong: %+v", got[0])
	}
	if got[0].Labels["modelName"] != "NVIDIA A100-SXM4-80GB" {
		t.Errorf("label with spaces lost: %q", got[0].Labels["modelName"])
	}
	if got[1].Labels["pod"] != `train "a"` || got[1].TimestampMs != 1700000000000 {
		t.Errorf("escaped label or timestamp wrong: %+v", got[1])
	}
	if !math.IsNaN(got[2].Value) {
		t.Errorf("NaN not parsed")
	}
	if got[3].Name != "up" || len(got[3].Labels) != 0 {
		t.Errorf("label-less sample wrong: %+v", got[3])
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{
		`metric{a="1" 1`,
		`metric{a=1} 1`,
		`metric{a="1"}`,
		`metric{a="1"} abc`,
	} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestEscapeRoundTrip(t *testing.T) {
	v := "a\"b\\c\nd"
	line := `m{k="` + EscapeLabel(v) + `"} 1`
	got, err := Parse(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Labels["k"] != v {
		t.Errorf("round trip: got %q want %q", got[0].Labels["k"], v)
	}
}
