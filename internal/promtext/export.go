package promtext

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Samples exported from a Prometheus-compatible store carry the time they were
// scraped. Two formats are read here: an HTTP API query result (what
// scripts/path-a/infrapulse-export.sh writes) and `promtool tsdb dump` output.

// ParseAPI reads a Prometheus HTTP API query response (/api/v1/query or
// /api/v1/query_range) holding a matrix or a vector. Every sample has
// TimestampMs set; all samples of a series share one Labels map.
func ParseAPI(r io.Reader) ([]Sample, error) {
	var doc struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a Prometheus query result: %w", err)
	}
	if doc.Status != "success" {
		return nil, fmt.Errorf("not a successful Prometheus query result (status %q %s)", doc.Status, doc.Error)
	}
	if t := doc.Data.ResultType; t != "matrix" && t != "vector" {
		return nil, fmt.Errorf("result type %q; want matrix or vector", t)
	}
	var out []Sample
	for _, s := range doc.Data.Result {
		name := s.Metric["__name__"]
		if name == "" {
			continue
		}
		delete(s.Metric, "__name__")
		points := s.Values
		if s.Value != nil {
			points = append(points, s.Value)
		}
		for _, p := range points {
			if len(p) != 2 {
				return nil, fmt.Errorf("%s: a sample is not a [time, value] pair", name)
			}
			ts, ok1 := p[0].(float64)
			vs, ok2 := p[1].(string)
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("%s: a sample is not a [time, \"value\"] pair", name)
			}
			v, err := parseValue(vs)
			if err != nil {
				return nil, fmt.Errorf("bad value %q for %s: %w", vs, name, err)
			}
			out = append(out, Sample{Name: name, Labels: s.Metric, Value: v, TimestampMs: int64(math.Round(ts * 1000))})
		}
	}
	return out, nil
}

// ParseDump reads `promtool tsdb dump` output, one sample per line:
//
//	{__name__="DCGM_FI_DEV_GPU_UTIL", gpu="0", ...} 97 1795414866534
func ParseDump(r io.Reader) ([]Sample, error) {
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
		if line[0] != '{' {
			return nil, fmt.Errorf("line %d: not a tsdb dump line (want {labels} value timestamp)", lineNo)
		}
		s := Sample{Labels: map[string]string{}}
		end, err := parseLabels(line, 1, s.Labels)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		rest := strings.Fields(line[end:])
		if len(rest) != 2 {
			return nil, fmt.Errorf("line %d: want a value and a timestamp after the labels", lineNo)
		}
		if s.Value, err = parseValue(rest[0]); err != nil {
			return nil, fmt.Errorf("line %d: bad value %q", lineNo, rest[0])
		}
		if s.TimestampMs, err = strconv.ParseInt(rest[1], 10, 64); err != nil {
			return nil, fmt.Errorf("line %d: bad timestamp %q", lineNo, rest[1])
		}
		s.Name = s.Labels["__name__"]
		delete(s.Labels, "__name__")
		if s.Name != "" {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}
