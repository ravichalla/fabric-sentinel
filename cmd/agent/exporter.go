package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"fabric-sentinel/pkg/metrics"
	"fabric-sentinel/pkg/scorer"
)

const metricPrefix = "fabric_sentinel_"

var allStates = []scorer.State{scorer.Healthy, scorer.Suspect, scorer.Unhealthy}

// exporter holds the latest scoring results and serves them over HTTP.
type exporter struct {
	node   string
	maxAge time.Duration
	now    func() time.Time

	mu            sync.RWMutex
	links         []scorer.LinkHealth
	updated       time.Time
	collectErrors uint64
	lastErr       string
}

func newExporter(node string, maxAge time.Duration) *exporter {
	return &exporter{node: node, maxAge: maxAge, now: time.Now}
}

func (e *exporter) publish(links []scorer.LinkHealth) {
	sort.Slice(links, func(i, j int) bool { return links[i].Link < links[j].Link })
	e.mu.Lock()
	e.links, e.updated, e.lastErr = links, e.now(), ""
	e.mu.Unlock()
}

func (e *exporter) fail(err error) {
	e.mu.Lock()
	e.collectErrors++
	e.lastErr = err.Error()
	e.mu.Unlock()
}

func (e *exporter) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", e.handleMetrics)
	mux.HandleFunc("/status", e.handleStatus)
	mux.HandleFunc("/healthz", e.handleHealthz)
	return mux
}

func (e *exporter) snapshot() ([]scorer.LinkHealth, time.Time, uint64, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]scorer.LinkHealth(nil), e.links...), e.updated, e.collectErrors, e.lastErr
}

func (e *exporter) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	links, updated, errs, _ := e.snapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = buildRegistry(e.node, links, updated, errs).WriteTo(w)
}

func (e *exporter) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	_, updated, _, lastErr := e.snapshot()
	if updated.IsZero() || e.now().Sub(updated) > e.maxAge {
		msg := "no recent successful collection"
		if lastErr != "" {
			msg += ": " + lastErr
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

type linkJSON struct {
	Link    string             `json:"link"`
	Ready   bool               `json:"ready"`
	Score   float64            `json:"score"`
	State   string             `json:"state"`
	Flaps   int                `json:"flaps"`
	Rates   map[string]float64 `json:"rates_per_sec"`
	Reasons []string           `json:"reasons"`
}

type statusJSON struct {
	Node      string     `json:"node"`
	Score     float64    `json:"score"`
	State     string     `json:"state"`
	WorstLink string     `json:"worst_link"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastError string     `json:"last_error,omitempty"`
	Links     []linkJSON `json:"links"`
}

func (e *exporter) handleStatus(w http.ResponseWriter, _ *http.Request) {
	links, updated, _, lastErr := e.snapshot()
	n := scorer.Aggregate(e.node, links)
	doc := statusJSON{
		Node: e.node, Score: round1(n.Score), State: n.State.String(), WorstLink: n.WorstLink,
		UpdatedAt: updated, LastError: lastErr, Links: make([]linkJSON, 0, len(links)),
	}
	for _, l := range links {
		reasons := l.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		doc.Links = append(doc.Links, linkJSON{
			Link: l.Link, Ready: l.Ready, Score: round1(l.Score), State: l.State.String(),
			Flaps: l.Flaps, Rates: l.Rates, Reasons: reasons,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}

// buildRegistry converts scoring results into Prometheus metric families.
func buildRegistry(node string, links []scorer.LinkHealth, updated time.Time, collectErrors uint64) *metrics.Registry {
	r := metrics.NewRegistry()
	nodeHealth := scorer.Aggregate(node, links)

	r.Add(metricPrefix+"node_health_score", "Node health score (0-100); the worst link decides.",
		metrics.Gauge, map[string]string{"node": node}, nodeHealth.Score)
	for _, st := range allStates {
		r.Add(metricPrefix+"node_state", "1 for the node's current state, 0 otherwise.",
			metrics.Gauge, map[string]string{"node": node, "state": st.String()}, b2f(nodeHealth.State == st))
	}

	for _, l := range links {
		lbl := map[string]string{"node": node, "link": l.Link}
		r.Add(metricPrefix+"link_health_score", "Link health score (0-100).", metrics.Gauge, lbl, l.Score)
		r.Add(metricPrefix+"link_flaps", "Link-down intervals inside the flap window.", metrics.Gauge, lbl, float64(l.Flaps))
		for _, st := range allStates {
			r.Add(metricPrefix+"link_state", "1 for the link's current state, 0 otherwise.",
				metrics.Gauge, map[string]string{"node": node, "link": l.Link, "state": st.String()}, b2f(l.State == st))
		}
		for name, rate := range l.Rates {
			r.Add(metricPrefix+"link_counter_rate", "Smoothed per-second rate of a scored counter.",
				metrics.Gauge, map[string]string{"node": node, "link": l.Link, "counter": name}, rate)
		}
		for name, v := range l.Raw {
			r.Add(metricPrefix+"link_counter_total", "Raw cumulative NIC/link counter.",
				metrics.Counter, map[string]string{"node": node, "link": l.Link, "counter": name}, float64(v))
		}
	}

	r.Add(metricPrefix+"collect_errors_total", "Failed collection cycles.", metrics.Counter,
		map[string]string{"node": node}, float64(collectErrors))
	if !updated.IsZero() {
		r.Add(metricPrefix+"last_collect_timestamp_seconds", "Unix time of the last successful collection.",
			metrics.Gauge, map[string]string{"node": node}, float64(updated.Unix()))
	}
	return r
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
