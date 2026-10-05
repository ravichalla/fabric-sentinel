// Package metrics renders the Prometheus text exposition format using only the
// standard library, so the agent has no third-party dependencies.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Type is a Prometheus metric type.
type Type string

const (
	Gauge   Type = "gauge"
	Counter Type = "counter"
)

// Series is one labelled value of a metric family.
type Series struct {
	Labels map[string]string
	Value  float64
}

// Family is a named metric with its help text, type and series.
type Family struct {
	Name   string
	Help   string
	Type   Type
	Series []Series
}

// Registry is an ordered collection of metric families that is rebuilt on
// every scrape cycle and then rendered.
type Registry struct {
	families []*Family
	index    map[string]*Family
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{index: make(map[string]*Family)}
}

// Add appends a series to the named family, creating it on first use.
func (r *Registry) Add(name, help string, t Type, labels map[string]string, value float64) {
	f, ok := r.index[name]
	if !ok {
		f = &Family{Name: name, Help: help, Type: t}
		r.index[name] = f
		r.families = append(r.families, f)
	}
	f.Series = append(f.Series, Series{Labels: labels, Value: value})
}

// WriteTo renders the registry. Families keep insertion order and series are
// sorted by their label string so output is stable between scrapes.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	var sb strings.Builder
	for _, f := range r.families {
		fmt.Fprintf(&sb, "# HELP %s %s\n", f.Name, escapeHelp(f.Help))
		fmt.Fprintf(&sb, "# TYPE %s %s\n", f.Name, f.Type)

		lines := make([]string, 0, len(f.Series))
		for _, s := range f.Series {
			lines = append(lines, f.Name+formatLabels(s.Labels)+" "+formatValue(s.Value)+"\n")
		}
		sort.Strings(lines)
		for _, l := range lines {
			sb.WriteString(l)
		}
	}
	n, err := io.WriteString(w, sb.String())
	return int64(n), err
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+`="`+escapeLabel(labels[k])+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}
