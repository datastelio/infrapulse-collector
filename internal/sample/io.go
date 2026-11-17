package sample

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Writer appends samples to a file as CSV or JSON Lines.
type Writer struct {
	f     *os.File
	bw    *bufio.Writer
	csv   *csv.Writer // nil for jsonl
	empty bool
}

// Create opens path for appending in the given format ("csv" or "jsonl").
// A CSV header is written only when the file is new or empty.
func Create(path, format string) (*Writer, error) {
	if format != "csv" && format != "jsonl" {
		return nil, fmt.Errorf("unknown format %q (want csv or jsonl)", format)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	w := &Writer{f: f, bw: bufio.NewWriter(f), empty: st.Size() == 0}
	if format == "csv" {
		w.csv = csv.NewWriter(w.bw)
	}
	return w, nil
}

// Write adds one sample.
func (w *Writer) Write(s Sample) error {
	rec := s.Record()
	if w.csv != nil {
		if w.empty {
			if err := w.csv.Write(Columns); err != nil {
				return err
			}
			w.empty = false
		}
		return w.csv.Write(rec)
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, col := range Columns {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(col)
		b.Write(k)
		b.WriteByte(':')
		switch {
		case numeric[col] && rec[i] == "":
			b.WriteString("null")
		case numeric[col]:
			b.WriteString(rec[i])
		default:
			v, _ := json.Marshal(rec[i])
			b.Write(v)
		}
	}
	b.WriteString("}\n")
	_, err := w.bw.WriteString(b.String())
	return err
}

// Flush pushes buffered rows to the file so readers see them.
func (w *Writer) Flush() error {
	if w.csv != nil {
		w.csv.Flush()
		if err := w.csv.Error(); err != nil {
			return err
		}
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

// Close flushes and closes the file.
func (w *Writer) Close() error {
	ferr := w.Flush()
	if err := w.f.Close(); err != nil {
		return err
	}
	return ferr
}

// Row is one sample as read back from a file, keyed by column name.
type Row map[string]string

// ReadFile reads a collector output file. The format is detected from the
// content: JSON Lines if the first non-blank byte is '{', otherwise CSV.
// Malformed rows (for example a partly written last line) are skipped.
func ReadFile(path string) ([]Row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
		return readJSONL(data), nil
	}
	return readCSV(data)
}

func readCSV(data []byte) ([]Row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}
	var rows []Row
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) != len(header) {
			continue
		}
		row := make(Row, len(header))
		for i, h := range header {
			row[h] = rec[i]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func readJSONL(data []byte) []Row {
	var rows []Row
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		row := make(Row, len(m))
		for k, v := range m {
			switch v := v.(type) {
			case nil:
				row[k] = ""
			case string:
				row[k] = v
			case float64:
				row[k] = strconv.FormatFloat(v, 'f', -1, 64)
			default:
				row[k] = fmt.Sprint(v)
			}
		}
		rows = append(rows, row)
	}
	return rows
}
