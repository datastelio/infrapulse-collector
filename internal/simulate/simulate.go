// Package simulate generates plausible fake samples for two GPUs so the
// collectors can be tried on a machine without one.
package simulate

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/sample"
)

type profile struct {
	model     string
	memTotal  float64 // MiB
	powerCap  float64 // W
	maxClock  float64 // MHz
	idlePower float64 // W
	powerCode string  // throttle_reasons value when power-capped
}

var profiles = map[string]profile{
	"nvidia": {"NVIDIA A100-SXM4-80GB", 81920, 400, 1410, 60, "sw_power_cap"},
	"amd":    {"AMD Instinct MI250X", 65536, 500, 1700, 90, "throttled"},
}

// Source is a fake two-GPU source. GPU 0 runs a steady training-like load
// that sometimes hits its power cap; GPU 1 alternates between idle and busy.
type Source struct {
	vendor string
	p      profile
	start  time.Time
	rng    *rand.Rand
}

// New returns a simulated source for "nvidia" or "amd".
func New(vendor string) *Source {
	p, ok := profiles[vendor]
	if !ok {
		p = profiles["nvidia"]
	}
	return &Source{vendor: vendor, p: p, start: time.Now(), rng: rand.New(rand.NewPCG(rand.Uint64(), 1))}
}

// Describe implements collector.Source.
func (s *Source) Describe() string {
	return fmt.Sprintf("simulated (2 fake %s GPUs, no hardware queried)", s.vendor)
}

// Query implements collector.Source.
func (s *Source) Query(ctx context.Context) ([]sample.Sample, error) {
	now := time.Now().UTC()
	t := now.Sub(s.start).Seconds()
	out := make([]sample.Sample, 2)
	for i := range out {
		var util float64
		if i == 0 {
			util = 92 + 6*math.Sin(t/30) + s.noise(2)
		} else if math.Mod(t, 120) < 60 {
			util = s.noise(1) + 1
		} else {
			util = 70 + s.noise(10)
		}
		util = clamp(util, 0, 100)
		power := s.p.idlePower + (s.p.powerCap-s.p.idlePower)*util/100*1.05 + s.noise(5)
		clock, throttle := s.p.maxClock, "none"
		if power >= s.p.powerCap {
			power = s.p.powerCap
			clock = s.p.maxClock * 0.88
			throttle = s.p.powerCode
		} else if util < 5 {
			clock = 210
			if s.vendor == "nvidia" {
				throttle = "gpu_idle"
			}
		}
		mem := s.p.memTotal * clamp(0.1+util/110, 0, 0.98)
		out[i] = sample.Sample{
			Time:       now,
			Vendor:     s.vendor,
			Index:      i,
			Model:      s.p.model,
			ID:         fmt.Sprintf("GPU-00000000-0000-4000-8000-00000000000%d", i),
			UtilGPU:    sample.FormatNum(math.Round(util)),
			UtilMem:    sample.FormatNum(math.Round(util * 0.6)),
			MemUsed:    sample.FormatNum(math.Round(mem)),
			MemTotal:   sample.FormatNum(s.p.memTotal),
			Temp:       sample.FormatNum(math.Round(35 + util*0.4 + s.noise(1))),
			Power:      sample.FormatNum(power),
			PowerLimit: sample.FormatNum(s.p.powerCap),
			ClockSM:    sample.FormatNum(math.Round(clock)),
			ClockSMMax: sample.FormatNum(s.p.maxClock),
			Throttle:   throttle,
		}
	}
	return out, nil
}

func (s *Source) noise(scale float64) float64 { return s.rng.NormFloat64() * scale }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
