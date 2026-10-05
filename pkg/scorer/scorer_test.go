package scorer

import (
	"context"
	"testing"
	"time"

	"fabric-sentinel/pkg/counters"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// run drives a scenario through the scorer and returns the per-tick verdicts.
func run(t *testing.T, sc counters.Scenario, ticks int) []LinkHealth {
	t.Helper()
	sim := counters.NewSimulator("node-a",
		[]counters.LinkSpec{{Name: "mlx5_0/1", Scenario: sc}}, t0, 5*time.Second, 42)
	sc2 := New(DefaultConfig())
	var out []LinkHealth
	for i := 0; i < ticks; i++ {
		samples, err := sim.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, sc2.Observe(samples[0]))
	}
	return out
}

func TestFirstSampleIsNotReady(t *testing.T) {
	h := run(t, counters.ScenarioHealthy, 1)
	if h[0].Ready {
		t.Fatal("first sample cannot produce rates; Ready should be false")
	}
	if h[0].Score != 100 || h[0].State != Healthy {
		t.Fatalf("warming-up link should default to healthy, got %v %v", h[0].Score, h[0].State)
	}
}

func TestHealthyLinkStaysHealthy(t *testing.T) {
	for i, h := range run(t, counters.ScenarioHealthy, 200) {
		if h.State != Healthy {
			t.Fatalf("tick %d: healthy link became %v (score %.1f, reasons %v)", i, h.State, h.Score, h.Reasons)
		}
	}
}

func TestCongestionAloneIsNotAFault(t *testing.T) {
	for i, h := range run(t, counters.ScenarioCongested, 100) {
		if h.State != Healthy || h.Score < 90 {
			t.Fatalf("tick %d: congestion misclassified as fault: %v %.1f %v", i, h.State, h.Score, h.Reasons)
		}
	}
}

func TestDegradingLinkProgressesThroughSuspectToUnhealthy(t *testing.T) {
	var sawSuspect, sawUnhealthy bool
	var firstSuspect, firstUnhealthy int
	for i, h := range run(t, counters.ScenarioDegrading, 60) {
		if h.State == Suspect && !sawSuspect {
			sawSuspect, firstSuspect = true, i
		}
		if h.State == Unhealthy && !sawUnhealthy {
			sawUnhealthy, firstUnhealthy = true, i
		}
	}
	if !sawSuspect || !sawUnhealthy {
		t.Fatalf("expected Suspect then Unhealthy (suspect=%v unhealthy=%v)", sawSuspect, sawUnhealthy)
	}
	if firstSuspect >= firstUnhealthy {
		t.Fatalf("expected Suspect (tick %d) before Unhealthy (tick %d)", firstSuspect, firstUnhealthy)
	}
}

func TestFlappingLinkIsDetectedAsHardFailure(t *testing.T) {
	hs := run(t, counters.ScenarioFlapping, 40)
	last := hs[len(hs)-1]
	if last.State != Unhealthy {
		t.Fatalf("persistently flapping link should be Unhealthy, got %v (score %.1f)", last.State, last.Score)
	}
	if last.Flaps < 2 {
		t.Fatalf("expected multiple flaps in window, got %d", last.Flaps)
	}
}

func TestHysteresisDelaysRecovery(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RecoverSamples = 3
	s := New(cfg)

	mk := func(i int, sym uint64) counters.Sample {
		return counters.Sample{
			Time: t0.Add(time.Duration(i) * 10 * time.Second), Node: "n", Link: "l",
			Values: map[string]uint64{counters.SymbolError: sym, counters.LinkDowned: 0},
		}
	}
	// Establish a baseline, then inject a burst of errors big enough to be Unhealthy.
	s.Observe(mk(0, 0))
	var sym uint64
	var h LinkHealth
	for i := 1; i <= 6; i++ {
		sym += 2000 // 200/s, far above crit
		h = s.Observe(mk(i, sym))
	}
	if h.State == Healthy {
		t.Fatalf("burst should have worsened state, got %v", h.State)
	}
	// Errors stop. The EWMA decays, but state must not improve instantly.
	h = s.Observe(mk(7, sym))
	if h.State == Healthy {
		t.Fatalf("state recovered immediately; hysteresis not applied")
	}
	// Eventually it recovers fully.
	for i := 8; i < 60; i++ {
		h = s.Observe(mk(i, sym))
	}
	if h.State != Healthy {
		t.Fatalf("expected recovery to Healthy after errors stop, got %v (score %.1f)", h.State, h.Score)
	}
}

func TestCounterResetIsIgnored(t *testing.T) {
	s := New(DefaultConfig())
	mk := func(i int, sym uint64) counters.Sample {
		return counters.Sample{
			Time: t0.Add(time.Duration(i) * 5 * time.Second), Node: "n", Link: "l",
			Values: map[string]uint64{counters.SymbolError: sym},
		}
	}
	s.Observe(mk(0, 1_000_000))
	h := s.Observe(mk(1, 5)) // driver reload: counter went backwards
	if h.Score != 100 || h.State != Healthy {
		t.Fatalf("counter reset must not be scored as an error burst: %v %.1f", h.State, h.Score)
	}
}

func TestAnomalyAnnotationOnSpike(t *testing.T) {
	cfg := DefaultConfig()
	s := New(cfg)
	mk := func(i int, sym uint64) counters.Sample {
		return counters.Sample{
			Time: t0.Add(time.Duration(i) * 5 * time.Second), Node: "n", Link: "l",
			Values: map[string]uint64{counters.SymbolError: sym},
		}
	}
	var sym uint64
	for i := 0; i < 20; i++ { // quiet baseline
		s.Observe(mk(i, sym))
	}
	sym += 500 // sudden 100/s spike
	h := s.Observe(mk(20, sym))
	var found bool
	for _, r := range h.Reasons {
		if len(r) > 8 && r[:8] == "anomaly:" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an anomaly annotation, reasons: %v", h.Reasons)
	}
}

func TestAggregateWorstLinkWins(t *testing.T) {
	n := Aggregate("n", []LinkHealth{
		{Link: "a", Score: 100, State: Healthy},
		{Link: "b", Score: 40, State: Unhealthy},
		{Link: "c", Score: 75, State: Suspect},
	})
	if n.State != Unhealthy || n.Score != 40 || n.WorstLink != "b" || n.Links != 3 {
		t.Fatalf("unexpected aggregate: %+v", n)
	}
}

func TestPenaltyForIsLinearAndCapped(t *testing.T) {
	r := Rule{Weight: 20, Warn: 10, Crit: 20}
	cases := []struct{ rate, want float64 }{{5, 0}, {10, 0}, {15, 10}, {20, 20}, {500, 20}}
	for _, c := range cases {
		if got := penaltyFor(r, c.rate); got != c.want {
			t.Errorf("penaltyFor(%v)=%v want %v", c.rate, got, c.want)
		}
	}
}
