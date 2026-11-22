// Package sink writes canonical gpu_samples rows to files.
package sink

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/fields"
	"github.com/datastelio/infrapulse-collector/internal/host"
	sample "github.com/datastelio/infrapulse-collector/internal/promsample"
)

// Writer writes rows and must be flushed before close.
type Writer interface {
	Write(rows []sample.Row) error
	Flush() error
}

// CSV writes the canonical schema with a fixed header. Missing values are
// written as empty cells.
type CSV struct {
	w       *csv.Writer
	cols    []string
	started bool
}

// NewCSV returns a CSV writer. The header is written on the first Write.
func NewCSV(out io.Writer) *CSV {
	return &CSV{w: csv.NewWriter(out), cols: fields.Columns()}
}

func (c *CSV) Write(rows []sample.Row) error {
	if !c.started {
		if err := c.w.Write(c.cols); err != nil {
			return err
		}
		c.started = true
	}
	rec := make([]string, len(c.cols))
	nIdent := len(fields.IdentityColumns)
	for _, r := range rows {
		rec[0] = r.TS.UTC().Format(time.RFC3339Nano)
		for i := 1; i < nIdent; i++ {
			rec[i] = r.Identity[c.cols[i]]
		}
		for i := nIdent; i < len(c.cols); i++ {
			if v, ok := r.Values[c.cols[i]]; ok {
				rec[i] = strconv.FormatFloat(v, 'g', -1, 64)
			} else {
				rec[i] = ""
			}
		}
		if err := c.w.Write(rec); err != nil {
			return err
		}
	}
	return nil
}

func (c *CSV) Flush() error {
	c.w.Flush()
	return c.w.Error()
}

// JSONL writes one JSON object per row, omitting missing values.
type JSONL struct {
	w *bufio.Writer
}

// NewJSONL returns a JSON Lines writer.
func NewJSONL(out io.Writer) *JSONL { return &JSONL{w: bufio.NewWriter(out)} }

func (j *JSONL) Write(rows []sample.Row) error {
	for _, r := range rows {
		obj := map[string]any{"ts": r.TS.UTC().Format(time.RFC3339Nano)}
		for k, v := range r.Identity {
			if v != "" {
				obj[k] = v
			}
		}
		for k, v := range r.Values {
			obj[k] = v
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		if _, err := j.w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (j *JSONL) Flush() error { return j.w.Flush() }

// New returns a writer for format "csv" or "jsonl".
func New(format string, out io.Writer) (Writer, error) {
	switch format {
	case "csv":
		return NewCSV(out), nil
	case "jsonl":
		return NewJSONL(out), nil
	}
	return nil, fmt.Errorf("unknown format %q (want csv or jsonl)", format)
}

// HostWriter writes host_samples rows.
type HostWriter interface {
	Write(rows []host.Row) error
	Flush() error
}

type hostCSV struct {
	w       *csv.Writer
	cols    []string
	started bool
}

func (c *hostCSV) Write(rows []host.Row) error {
	if !c.started {
		if err := c.w.Write(c.cols); err != nil {
			return err
		}
		c.started = true
	}
	rec := make([]string, len(c.cols))
	n := len(fields.HostIdentityColumns)
	for _, r := range rows {
		rec[0], rec[1], rec[2], rec[3] = r.TS.UTC().Format(time.RFC3339Nano), r.Host, r.Scope, r.ScopeID
		for i := n; i < len(c.cols); i++ {
			if v, ok := r.Values[c.cols[i]]; ok {
				rec[i] = strconv.FormatFloat(v, 'g', -1, 64)
			} else {
				rec[i] = ""
			}
		}
		if err := c.w.Write(rec); err != nil {
			return err
		}
	}
	return nil
}

func (c *hostCSV) Flush() error {
	c.w.Flush()
	return c.w.Error()
}

type hostJSONL struct{ w *bufio.Writer }

func (j *hostJSONL) Write(rows []host.Row) error {
	for _, r := range rows {
		obj := map[string]any{"ts": r.TS.UTC().Format(time.RFC3339Nano), "host": r.Host, "scope": r.Scope, "scope_id": r.ScopeID}
		for k, v := range r.Values {
			obj[k] = v
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		if _, err := j.w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (j *hostJSONL) Flush() error { return j.w.Flush() }

// NewHost returns a host_samples writer for format "csv" or "jsonl".
func NewHost(format string, out io.Writer) (HostWriter, error) {
	switch format {
	case "csv":
		return &hostCSV{w: csv.NewWriter(out), cols: fields.HostColumns()}, nil
	case "jsonl":
		return &hostJSONL{w: bufio.NewWriter(out)}, nil
	}
	return nil, fmt.Errorf("unknown format %q (want csv or jsonl)", format)
}
