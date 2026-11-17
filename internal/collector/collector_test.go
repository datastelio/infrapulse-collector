package collector

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/sample"
	"github.com/datastelio/infrapulse-collector/internal/simulate"
)

func TestHashID(t *testing.T) {
	a := HashID("salt", "GPU-1")
	if len(a) != 12 || a != HashID("salt", "GPU-1") || a == HashID("other", "GPU-1") || a == HashID("salt", "GPU-2") {
		t.Errorf("HashID = %q", a)
	}
	if HashID("salt", "") != "" {
		t.Error("empty UUID should stay empty")
	}
}

func TestRunWritesHashedIDs(t *testing.T) {
	for _, raw := range []bool{false, true} {
		out := filepath.Join(t.TempDir(), "s.jsonl")
		var log bytes.Buffer
		o := Options{Interval: 10 * time.Millisecond, Duration: 55 * time.Millisecond, Out: out, Format: "jsonl", RawUUID: raw}
		if err := Run(context.Background(), simulate.New("amd"), o, &log); err != nil {
			t.Fatal(err)
		}
		rows, err := sample.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) < 4 || len(rows)%2 != 0 {
			t.Fatalf("got %d rows", len(rows))
		}
		id := rows[0]["gpu_id"]
		if raw {
			if !strings.HasPrefix(id, "GPU-") || strings.Contains(log.String(), "salt") {
				t.Errorf("raw: id %q, log %q", id, log.String())
			}
			continue
		}
		if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id) || !strings.Contains(log.String(), "salt") {
			t.Errorf("hashed: id %q, log %q", id, log.String())
		}
		if rows[0]["gpu_id"] != rows[2]["gpu_id"] {
			t.Error("same GPU should keep the same id within a run")
		}
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- Run(ctx, simulate.New("nvidia"), Options{Interval: time.Hour, Out: filepath.Join(t.TempDir(), "s.csv"), Format: "csv"}, &bytes.Buffer{})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

type emptySource struct{}

func (emptySource) Describe() string                               { return "empty" }
func (emptySource) Query(context.Context) ([]sample.Sample, error) { return nil, nil }

func TestCheck(t *testing.T) {
	var out bytes.Buffer
	if code := Check(context.Background(), simulate.New("nvidia"), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "GPUs found: 2") || !strings.Contains(out.String(), "util_gpu_pct") {
		t.Errorf("output: %s", out.String())
	}
	out.Reset()
	if code := Check(context.Background(), emptySource{}, &out); code == 0 || !strings.Contains(out.String(), "no GPUs found") {
		t.Errorf("empty source: exit %d, %s", code, out.String())
	}
}
