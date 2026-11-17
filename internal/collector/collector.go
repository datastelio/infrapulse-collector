// Package collector holds the command-line front end shared by the vendor
// collectors: flags, the `check` subcommand, the sampling loop, gpu_id
// hashing and clean shutdown.
package collector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/sample"
	"github.com/datastelio/infrapulse-collector/internal/simulate"
)

// Source produces one sample per GPU each time it is queried. Sample.ID
// holds the GPU's raw UUID; the runner hashes it before writing.
type Source interface {
	Describe() string
	Query(ctx context.Context) ([]sample.Sample, error)
}

// Opener finds the vendor tool and returns a Source, or an error if the
// tool is not installed.
type Opener func(ctx context.Context) (Source, error)

// Options controls a collection run.
type Options struct {
	Interval time.Duration
	Duration time.Duration
	Out      string
	Format   string
	RawUUID  bool
}

// Main runs a collector command and returns its exit code.
func Main(name, vendor string, open Opener, args []string) int {
	check := len(args) > 0 && args[0] == "check"
	if check {
		args = args[1:]
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var o Options
	var simulated bool
	fs.DurationVar(&o.Interval, "interval", 10*time.Second, "time between samples")
	fs.DurationVar(&o.Duration, "duration", 0, "stop after this long (0 = until Ctrl-C)")
	fs.StringVar(&o.Out, "out", "", "output file (default ./gpu-samples.csv or ./gpu-samples.jsonl)")
	fs.StringVar(&o.Format, "format", "csv", "output format: csv or jsonl")
	fs.BoolVar(&o.RawUUID, "raw-uuid", false, "record the real GPU UUID instead of a salted hash")
	fs.BoolVar(&simulated, "simulate", false, "generate fake samples for 2 GPUs instead of querying hardware")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [check] [flags]\n\nSamples %s GPU metrics and appends them to a local file. Read-only; nothing is sent over the network.\n\n", name, vendor)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if o.Interval <= 0 {
		fmt.Fprintln(os.Stderr, "--interval must be positive")
		return 2
	}
	if o.Out == "" {
		o.Out = "gpu-samples." + o.Format
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var src Source
	if simulated {
		src = simulate.New(vendor)
	} else {
		var err error
		if src, err = open(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			return 1
		}
	}
	if check {
		return Check(ctx, src, os.Stdout)
	}
	if err := Run(ctx, src, o, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}

// Check queries the source once, lists the GPUs and which fields have
// values, and returns 1 if no GPUs were found.
func Check(ctx context.Context, src Source, w io.Writer) int {
	fmt.Fprintf(w, "source: %s\n", src.Describe())
	samples, err := src.Query(ctx)
	if err != nil {
		fmt.Fprintf(w, "query failed: %v\n", err)
		return 1
	}
	if len(samples) == 0 {
		fmt.Fprintln(w, "no GPUs found")
		return 1
	}
	fmt.Fprintf(w, "\nGPUs found: %d\n", len(samples))
	for _, s := range samples {
		mem := s.MemTotal
		if mem == "" {
			mem = "?"
		}
		fmt.Fprintf(w, "  [%d] %s (%s MiB)\n", s.Index, orDash(s.Model), mem)
	}
	fmt.Fprintln(w, "\nfields:")
	for i, col := range sample.Columns {
		if i < 5 { // timestamp, vendor, index, model, id are always written
			continue
		}
		n := 0
		for _, s := range samples {
			if s.Record()[i] != "" {
				n++
			}
		}
		state := "available"
		if n == 0 {
			state = "missing"
		} else if n < len(samples) {
			state = "partial"
		}
		fmt.Fprintf(w, "  %-18s %-10s %d/%d GPUs\n", col, state, n, len(samples))
	}
	return 0
}

// Run samples src every o.Interval until ctx is cancelled or o.Duration
// elapses, appending rows to o.Out and flushing after every sample.
func Run(ctx context.Context, src Source, o Options, log io.Writer) error {
	salt := ""
	if !o.RawUUID {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		salt = hex.EncodeToString(b)
		fmt.Fprintf(log, "gpu_id salt for this run (not stored in the file): %s\n", salt)
	}
	if o.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Duration)
		defer cancel()
	}
	w, err := sample.Create(o.Out, o.Format)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "sampling %s every %s into %s (Ctrl-C to stop)\n", src.Describe(), o.Interval, o.Out)

	tick := time.NewTicker(o.Interval)
	defer tick.Stop()
	rows := 0
	for {
		samples, err := src.Query(ctx)
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(log, "query failed: %v\n", err)
		}
		for _, s := range samples {
			if !o.RawUUID {
				s.ID = HashID(salt, s.ID)
			}
			if err := w.Write(s); err != nil {
				w.Close()
				return err
			}
			rows++
		}
		if err := w.Flush(); err != nil {
			w.Close()
			return err
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(log, "stopped; wrote %d rows to %s\n", rows, o.Out)
			return w.Close()
		case <-tick.C:
		}
	}
}

// HashID returns the first 12 hex characters of SHA-256(salt + uuid), or ""
// if uuid is empty.
func HashID(salt, uuid string) string {
	if uuid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(salt + uuid))
	return hex.EncodeToString(sum[:])[:12]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
