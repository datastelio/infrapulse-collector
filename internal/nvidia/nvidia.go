// Package nvidia samples NVIDIA GPUs through nvidia-smi.
package nvidia

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/collector"
	"github.com/datastelio/infrapulse-collector/internal/sample"
)

const baseFields = "timestamp,index,name,uuid,utilization.gpu,utilization.memory," +
	"memory.used,memory.total,temperature.gpu,power.draw,power.limit,clocks.sm,clocks.max.sm"

// reasonFields are tried in order: drivers before R535 only know the old name.
var reasonFields = []string{"clocks_event_reasons.active", "clocks_throttle_reasons.active"}

const numFields = 14

// runFunc runs nvidia-smi with args and returns its standard output.
type runFunc func(ctx context.Context, args ...string) ([]byte, error)

// Source queries nvidia-smi.
type Source struct {
	bin         string
	reasonField string
	run         runFunc
}

// Open finds nvidia-smi and picks the throttle-reason field the driver accepts.
func Open(ctx context.Context) (collector.Source, error) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, errors.New("nvidia-smi not found in PATH (it ships with the NVIDIA driver)")
	}
	return open(ctx, bin, execRun(bin))
}

func open(ctx context.Context, bin string, run runFunc) (*Source, error) {
	var errs []error
	for _, f := range reasonFields {
		if _, err := run(ctx, queryArgs(f)...); err != nil {
			errs = append(errs, err)
			continue
		}
		return &Source{bin: bin, reasonField: f, run: run}, nil
	}
	return nil, errors.Join(errs...)
}

func queryArgs(reasonField string) []string {
	return []string{"--query-gpu=" + baseFields + "," + reasonField, "--format=csv,noheader,nounits"}
}

func execRun(bin string) runFunc {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stdout.String() + " " + stderr.String())
			return nil, fmt.Errorf("nvidia-smi: %w: %s", err, msg)
		}
		return stdout.Bytes(), nil
	}
}

// Describe implements collector.Source.
func (s *Source) Describe() string {
	return fmt.Sprintf("nvidia-smi (%s), throttle field %s", s.bin, s.reasonField)
}

// Query implements collector.Source.
func (s *Source) Query(ctx context.Context) ([]sample.Sample, error) {
	out, err := s.run(ctx, queryArgs(s.reasonField)...)
	if err != nil {
		return nil, err
	}
	return Parse(out, time.Local, time.Now())
}

// Parse reads nvidia-smi CSV output (noheader, nounits). nvidia-smi prints
// local time without a zone, so loc says how to interpret it; now is used
// when the timestamp is missing or unreadable.
func Parse(out []byte, loc *time.Location, now time.Time) ([]sample.Sample, error) {
	r := csv.NewReader(bytes.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	var samples []sample.Sample
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing nvidia-smi output: %w", err)
		}
		if len(rec) < numFields {
			return nil, fmt.Errorf("nvidia-smi row has %d fields, want %d: %q", len(rec), numFields, strings.Join(rec, ","))
		}
		// A comma inside the GPU name splits it; rejoin the extra fields.
		if extra := len(rec) - numFields; extra > 0 {
			name := strings.Join(rec[2:3+extra], ",")
			rec = append(append(rec[:2:2], name), rec[3+extra:]...)
		}
		for i := range rec {
			rec[i] = clean(rec[i])
		}
		idx, err := strconv.Atoi(rec[1])
		if err != nil {
			return nil, fmt.Errorf("bad GPU index %q", rec[1])
		}
		ts := now
		if t, err := time.ParseInLocation("2006/01/02 15:04:05.000", rec[0], loc); err == nil {
			ts = t
		}
		samples = append(samples, sample.Sample{
			Time:       ts.UTC(),
			Vendor:     "nvidia",
			Index:      idx,
			Model:      rec[2],
			ID:         rec[3],
			UtilGPU:    sample.Num(rec[4]),
			UtilMem:    sample.Num(rec[5]),
			MemUsed:    sample.Num(rec[6]),
			MemTotal:   sample.Num(rec[7]),
			Temp:       sample.Num(rec[8]),
			Power:      sample.Num(rec[9]),
			PowerLimit: sample.Num(rec[10]),
			ClockSM:    sample.Num(rec[11]),
			ClockSMMax: sample.Num(rec[12]),
			Throttle:   Reasons(rec[13]),
		})
	}
	return samples, nil
}

// clean trims a value and blanks nvidia-smi placeholders such as "[N/A]"
// and "[Not Supported]".
func clean(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") || v == "N/A" {
		return ""
	}
	return v
}

var reasonBits = []struct {
	bit  uint64
	name string
}{
	{0x1, "gpu_idle"},
	{0x2, "applications_clocks_setting"},
	{0x4, "sw_power_cap"},
	{0x8, "hw_slowdown"},
	{0x10, "sync_boost"},
	{0x20, "sw_thermal_slowdown"},
	{0x40, "hw_thermal_slowdown"},
	{0x80, "hw_power_brake_slowdown"},
	{0x100, "display_clock_setting"},
}

// Reasons decodes the clocks_event_reasons bitmask ("0x...4") into names
// joined by "|": "none" for 0, "" if the value is missing. Unknown bits are
// kept as hex.
func Reasons(v string) string {
	if v == "" {
		return ""
	}
	mask, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(v), "0x"), 16, 64)
	if err != nil {
		return ""
	}
	if mask == 0 {
		return "none"
	}
	var names []string
	for _, r := range reasonBits {
		if mask&r.bit != 0 {
			names = append(names, r.name)
			mask &^= r.bit
		}
	}
	if mask != 0 {
		names = append(names, fmt.Sprintf("0x%x", mask))
	}
	return strings.Join(names, "|")
}
