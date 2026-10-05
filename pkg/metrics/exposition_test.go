package metrics

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestRegistryRendersPrometheusText(t *testing.T) {
	r := NewRegistry()
	r.Add("fs_score", "Health score.", Gauge, map[string]string{"node": "n1", "link": "mlx5_1/1"}, 42.5)
	r.Add("fs_score", "Health score.", Gauge, map[string]string{"node": "n1", "link": "mlx5_0/1"}, 100)
	r.Add("fs_errors_total", "Errors.", Counter, map[string]string{"node": "n1"}, 7)

	var buf bytes.Buffer
	if _, err := r.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	want := `# HELP fs_score Health score.
# TYPE fs_score gauge
fs_score{link="mlx5_0/1",node="n1"} 100
fs_score{link="mlx5_1/1",node="n1"} 42.5
# HELP fs_errors_total Errors.
# TYPE fs_errors_total counter
fs_errors_total{node="n1"} 7
`
	if buf.String() != want {
		t.Fatalf("unexpected output:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestLabelAndHelpEscaping(t *testing.T) {
	r := NewRegistry()
	r.Add("m", "line1\nline2 \\ end", Gauge, map[string]string{"k": "a\"b\\c\nd"}, 1)
	var buf bytes.Buffer
	if _, err := r.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `# HELP m line1\nline2 \\ end`) {
		t.Errorf("help not escaped: %q", out)
	}
	if !strings.Contains(out, `m{k="a\"b\\c\nd"} 1`) {
		t.Errorf("label not escaped: %q", out)
	}
}

func TestSpecialFloatValues(t *testing.T) {
	cases := map[float64]string{math.NaN(): "NaN", math.Inf(1): "+Inf", math.Inf(-1): "-Inf", 0.25: "0.25"}
	for in, want := range cases {
		if got := formatValue(in); got != want {
			t.Errorf("formatValue(%v)=%q want %q", in, got, want)
		}
	}
}
