package view

import (
	"bytes"
	"strings"
	"testing"

	"github.com/datastelio/infrapulse-collector/internal/sample"
)

func TestLatestAndRender(t *testing.T) {
	rows := []sample.Row{
		{"timestamp": "2026-11-17T08:00:00Z", "vendor": "nvidia", "gpu_index": "10", "util_gpu_pct": "1"},
		{"timestamp": "2026-11-17T08:00:00Z", "vendor": "nvidia", "gpu_index": "2", "util_gpu_pct": "5"},
		{"timestamp": "2026-11-17T08:00:10Z", "vendor": "nvidia", "gpu_index": "2", "util_gpu_pct": "99",
			"mem_used_mib": "100", "mem_total_mib": "200", "power_limit_w": "400"},
	}
	got := Latest(rows)
	if len(got) != 2 || got[0]["gpu_index"] != "2" || got[0]["util_gpu_pct"] != "99" {
		t.Fatalf("Latest = %v", got)
	}
	var b bytes.Buffer
	if err := Render(&b, got); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "TIME (UTC)") {
		t.Fatalf("table:\n%s", b.String())
	}
	for _, want := range []string{"2026-11-17 08:00:10", "99", "100/200", "-/400"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q missing %q", lines[1], want)
		}
	}
}
