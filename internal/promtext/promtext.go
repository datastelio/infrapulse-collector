// Package promtext parses the Prometheus text exposition format produced by
// dcgm-exporter. It is intentionally small and dependency-free: the collector
// runs on customer hosts, so every dependency is something a security reviewer
// has to approve.
package promtext

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Sample is one line of exposition: a metric name, its labels and a value.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
	// TimestampMs is the optional exposition timestamp (0 if absent).
	TimestampMs int64
}

// Parse reads exposition text and returns every sample. Comment lines
// (# HELP, # TYPE) and blank lines are skipped. A malformed line returns an
// error that names the line number.
func Parse(r io.Reader) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseLine(line string) (Sample, error) {
	s := Sample{Labels: map[string]string{}}
	i := 0
	for i < len(line) && line[i] != '{' && line[i] != ' ' && line[i] != '\t' {
		i++
	}
	s.Name = line[:i]
	if s.Name == "" {
		return s, fmt.Errorf("missing metric name")
	}
	if i < len(line) && line[i] == '{' {
		end, err := parseLabels(line, i+1, s.Labels)
		if err != nil {
			return s, err
		}
		i = end
	}
	rest := strings.Fields(line[i:])
	if len(rest) == 0 {
		return s, fmt.Errorf("missing value for %s", s.Name)
	}
	v, err := parseValue(rest[0])
	if err != nil {
		return s, fmt.Errorf("bad value %q for %s: %w", rest[0], s.Name, err)
	}
	s.Value = v
	if len(rest) > 1 {
		ts, err := strconv.ParseInt(rest[1], 10, 64)
		if err != nil {
			return s, fmt.Errorf("bad timestamp %q for %s", rest[1], s.Name)
		}
		s.TimestampMs = ts
	}
	return s, nil
}

// parseLabels parses `k="v",k2="v2"}` starting at pos and returns the index
// just after the closing brace.
func parseLabels(line string, pos int, into map[string]string) (int, error) {
	i := pos
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == ',') {
			i++
		}
		if i >= len(line) {
			return 0, fmt.Errorf("unterminated label set")
		}
		if line[i] == '}' {
			return i + 1, nil
		}
		start := i
		for i < len(line) && line[i] != '=' {
			i++
		}
		if i >= len(line) {
			return 0, fmt.Errorf("label without value")
		}
		key := strings.TrimSpace(line[start:i])
		i++ // '='
		if i >= len(line) || line[i] != '"' {
			return 0, fmt.Errorf("label %s: expected quote", key)
		}
		i++
		var val strings.Builder
		for {
			if i >= len(line) {
				return 0, fmt.Errorf("label %s: unterminated value", key)
			}
			c := line[i]
			if c == '\\' && i+1 < len(line) {
				switch line[i+1] {
				case 'n':
					val.WriteByte('\n')
				case '"':
					val.WriteByte('"')
				case '\\':
					val.WriteByte('\\')
				default:
					val.WriteByte(line[i+1])
				}
				i += 2
				continue
			}
			if c == '"' {
				i++
				break
			}
			val.WriteByte(c)
			i++
		}
		into[key] = val.String()
	}
}

func parseValue(s string) (float64, error) {
	switch s {
	case "NaN":
		return math.NaN(), nil
	case "+Inf", "Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(s, 64)
}

// EscapeLabel escapes a label value for exposition output.
func EscapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}
