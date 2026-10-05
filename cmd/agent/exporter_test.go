package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fabric-sentinel/pkg/counters"
	"fabric-sentinel/pkg/scorer"
)

// scoredExporter runs the simulator through the scorer and loads the result
// into an exporter, exactly as the agent loop does.
func scoredExporter(t *testing.T, ticks int) *exporter {
	t.Helper()
	specs, err := counters.ParseLinkSpecs("mlx5_0/1=healthy,mlx5_1/1=flapping")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sim := counters.NewSimulator("node-a", specs, start, 5*time.Second, 1)
	sc := scorer.New(scorer.DefaultConfig())
	exp := newExporter("node-a", time.Minute)
	exp.now = func() time.Time { return start }

	var links []scorer.LinkHealth
	for i := 0; i < ticks; i++ {
		samples, _ := sim.Collect(context.Background())
		links = links[:0]
		for _, s := range samples {
			links = append(links, sc.Observe(s))
		}
	}
	exp.publish(append([]scorer.LinkHealth(nil), links...))
	return exp
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestMetricsEndpoint(t *testing.T) {
	exp := scoredExporter(t, 30)
	rec := get(t, exp.routes(), "/metrics")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`fabric_sentinel_link_health_score{link="mlx5_0/1",node="node-a"} 100`,
		`fabric_sentinel_link_state{link="mlx5_1/1",node="node-a",state="Unhealthy"} 1`,
		`fabric_sentinel_link_state{link="mlx5_1/1",node="node-a",state="Healthy"} 0`,
		`fabric_sentinel_node_state{node="node-a",state="Unhealthy"} 1`,
		`# TYPE fabric_sentinel_link_counter_total counter`,
		`fabric_sentinel_collect_errors_total{node="node-a"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestStatusEndpoint(t *testing.T) {
	exp := scoredExporter(t, 30)
	rec := get(t, exp.routes(), "/status")
	var doc statusJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, rec.Body.String())
	}
	if doc.State != "Unhealthy" || doc.WorstLink != "mlx5_1/1" || len(doc.Links) != 2 {
		t.Fatalf("unexpected status: %+v", doc)
	}
	if len(doc.Links[1].Reasons) == 0 {
		t.Error("flapping link should carry human-readable reasons")
	}
}

func TestHealthz(t *testing.T) {
	exp := newExporter("n", time.Minute)
	if rec := get(t, exp.routes(), "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before first collect want 503, got %d", rec.Code)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exp.now = func() time.Time { return base }
	exp.publish(nil)
	if rec := get(t, exp.routes(), "/healthz"); rec.Code != 200 {
		t.Fatalf("after collect want 200, got %d", rec.Code)
	}
	exp.now = func() time.Time { return base.Add(2 * time.Minute) }
	if rec := get(t, exp.routes(), "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale data want 503, got %d", rec.Code)
	}
}

func TestNewSourceRejectsUnknownMode(t *testing.T) {
	if _, err := newSource("bogus", "n", "", "", time.Second, 1); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}
