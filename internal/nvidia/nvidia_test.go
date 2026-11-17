package nvidia

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	out, err := os.ReadFile("testdata/nvidia-smi.csv")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(out, time.UTC, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 3", len(got))
	}
	g := got[0]
	if g.Time != time.Date(2026, 11, 17, 8, 0, 0, 123e6, time.UTC) {
		t.Errorf("time = %v", g.Time)
	}
	if g.Vendor != "nvidia" || g.Index != 0 || g.Model != "NVIDIA A100-SXM4-80GB" ||
		g.ID != "GPU-11111111-2222-4333-8444-555555555500" {
		t.Errorf("identity = %+v", g)
	}
	want := []string{"97", "41", "61234", "81920", "64", "398.51", "400", "1275", "1410", "sw_power_cap"}
	have := []string{g.UtilGPU, g.UtilMem, g.MemUsed, g.MemTotal, g.Temp, g.Power, g.PowerLimit, g.ClockSM, g.ClockSMMax, g.Throttle}
	if strings.Join(have, ",") != strings.Join(want, ",") {
		t.Errorf("values = %v, want %v", have, want)
	}
	if got[1].Throttle != "gpu_idle" {
		t.Errorf("gpu 1 throttle = %q", got[1].Throttle)
	}
	// [N/A] and [Not Supported] become empty.
	if g := got[2]; g.Power != "" || g.PowerLimit != "" || g.Throttle != "" || g.ClockSMMax != "3105" {
		t.Errorf("gpu 2 = %+v", g)
	}
}

func TestParseCommaInName(t *testing.T) {
	line := "2026/11/17 08:00:00.000, 0, Weird, Name GPU, GPU-x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0x0\n"
	got, err := Parse([]byte(line), time.UTC, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Model != "Weird,Name GPU" || got[0].ID != "GPU-x" || got[0].Throttle != "none" {
		t.Errorf("got %+v", got[0])
	}
}

func TestParseShortRow(t *testing.T) {
	if _, err := Parse([]byte("a, b, c\n"), time.UTC, time.Time{}); err == nil {
		t.Error("want error for a short row")
	}
}

func TestReasons(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"0x0000000000000000": "none",
		"0x0000000000000044": "sw_power_cap|hw_thermal_slowdown",
		"0x0000000000001001": "gpu_idle|0x1000",
		"garbage":            "",
	}
	for in, want := range cases {
		if got := Reasons(in); got != want {
			t.Errorf("Reasons(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenFallsBackToThrottleField(t *testing.T) {
	var asked []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		asked = append(asked, args[0])
		if strings.Contains(args[0], "clocks_event_reasons") {
			return nil, errors.New(`Field "clocks_event_reasons.active" is not a valid field to query.`)
		}
		return nil, nil
	}
	s, err := open(context.Background(), "nvidia-smi", run)
	if err != nil {
		t.Fatal(err)
	}
	if s.reasonField != "clocks_throttle_reasons.active" || len(asked) != 2 {
		t.Errorf("field = %q after %d queries", s.reasonField, len(asked))
	}
}

func TestOpenFailsWhenBothFieldsFail(t *testing.T) {
	run := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("No devices were found") }
	if _, err := open(context.Background(), "nvidia-smi", run); err == nil {
		t.Error("want error")
	}
}
