package counters

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseLinkSpecs(t *testing.T) {
	got, err := ParseLinkSpecs("mlx5_0/1=healthy, mlx5_1/1=degrading")
	if err != nil {
		t.Fatal(err)
	}
	want := []LinkSpec{{"mlx5_0/1", ScenarioHealthy}, {"mlx5_1/1", ScenarioDegrading}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, bad := range []string{"", "x", "x=bogus", "=healthy"} {
		if _, err := ParseLinkSpecs(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestSimulatorIsDeterministicAndMonotonic(t *testing.T) {
	specs := []LinkSpec{{"l0", ScenarioDegrading}, {"l1", ScenarioHealthy}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := NewSimulator("n", specs, start, 5*time.Second, 7)
	b := NewSimulator("n", specs, start, 5*time.Second, 7)

	var prev uint64
	for i := 0; i < 30; i++ {
		sa, _ := a.Collect(context.Background())
		sb, _ := b.Collect(context.Background())
		if !reflect.DeepEqual(sa, sb) {
			t.Fatalf("tick %d: same seed produced different output", i)
		}
		if got, want := sa[0].Time, start.Add(time.Duration(i+1)*5*time.Second); !got.Equal(want) {
			t.Fatalf("tick %d: time %v want %v", i, got, want)
		}
		v := sa[0].Values[SymbolError]
		if v < prev {
			t.Fatalf("tick %d: cumulative counter went backwards (%d -> %d)", i, prev, v)
		}
		prev = v
	}
}

func writeCounter(t *testing.T, dir, name, val string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(val), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSysfsCollect(t *testing.T) {
	root := t.TempDir()
	port := filepath.Join(root, "mlx5_0", "ports", "1")
	writeCounter(t, filepath.Join(port, "counters"), "symbol_error", "12\n")
	writeCounter(t, filepath.Join(port, "counters"), "link_downed", "3\n")
	writeCounter(t, filepath.Join(port, "hw_counters"), "out_of_sequence", "99\n")
	writeCounter(t, filepath.Join(port, "hw_counters"), "weird", "N/A\n") // unparsable: skipped

	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &Sysfs{Root: root, Node: "node-a", Now: func() time.Time { return fixed }}
	got, err := src.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 sample, got %d", len(got))
	}
	s := got[0]
	if s.Link != "mlx5_0/1" || s.Node != "node-a" || !s.Time.Equal(fixed) {
		t.Fatalf("bad sample metadata: %+v", s)
	}
	want := map[string]uint64{"symbol_error": 12, "link_downed": 3, "out_of_sequence": 99}
	if !reflect.DeepEqual(s.Values, want) {
		t.Fatalf("values: got %v want %v", s.Values, want)
	}
}

func TestSysfsNoDevices(t *testing.T) {
	src := &Sysfs{Root: t.TempDir(), Node: "n"}
	if _, err := src.Collect(context.Background()); err == nil {
		t.Fatal("expected an error when no RDMA ports are present")
	}
	if _, err := (&Sysfs{Root: "/definitely/not/here"}).Collect(context.Background()); err == nil {
		t.Fatal("expected an error for a missing sysfs root")
	}
}
