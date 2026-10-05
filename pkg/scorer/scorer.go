// Package scorer turns cumulative NIC/link counters into a per-link and
// per-node health score.
//
// The model separates two failure shapes that matter for training fabrics:
//
//   - Hard failures: the link goes down. Detected by counting link-down events
//     inside a sliding window of recent samples (flap detection).
//   - Slow degradation: error and discard rates creep up while the link stays
//     "up". Detected by comparing an EWMA-smoothed per-second rate against
//     warn/crit thresholds, with a z-score check to annotate sudden spikes.
//
// State changes use hysteresis: a link gets worse immediately but only
// recovers after several consecutive better samples, so a flapping link does
// not bounce between states.
//
// A Scorer is not safe for concurrent use; call Observe from one goroutine.
package scorer

import (
	"fmt"
	"math"

	"fabric-sentinel/pkg/counters"
)

// State is the coarse health classification of a link or node.
type State int

const (
	Healthy State = iota
	Suspect
	Unhealthy
)

func (s State) String() string {
	switch s {
	case Healthy:
		return "Healthy"
	case Suspect:
		return "Suspect"
	case Unhealthy:
		return "Unhealthy"
	}
	return "Unknown"
}

// Rule penalises a counter whose smoothed per-second rate exceeds Warn. The
// penalty grows linearly from 0 at Warn to Weight at Crit and is capped there.
type Rule struct {
	Counter string
	Weight  float64
	Warn    float64 // per-second rate where the penalty starts
	Crit    float64 // per-second rate where the full Weight applies
}

// Config tunes the scoring model.
type Config struct {
	Rules []Rule

	Alpha         float64 // EWMA smoothing for rates (0..1, higher = more reactive)
	BaselineAlpha float64 // slower EWMA used as the z-score baseline
	AnomalyZ      float64 // z-score at which a spike is annotated
	AnomalyWarmup int     // samples needed before anomalies are reported

	FlapCounter string  // cumulative counter that increments on link-down
	FlapWindow  int     // number of recent sample intervals to consider
	FlapPenalty float64 // score penalty per flapped interval in the window

	SuspectBelow   float64 // score below this is Suspect
	UnhealthyBelow float64 // score below this is Unhealthy
	RecoverSamples int     // consecutive better samples required to improve state
}

// DefaultConfig returns thresholds suited to the Linux RDMA counters. They are
// starting points: real fleets should tune them per NIC model and workload.
func DefaultConfig() Config {
	return Config{
		Rules: []Rule{
			{Counter: counters.SymbolError, Weight: 25, Warn: 0.5, Crit: 10},
			{Counter: counters.PortRcvErrors, Weight: 25, Warn: 0.1, Crit: 5},
			{Counter: counters.LinkErrorRecovery, Weight: 20, Warn: 0, Crit: 0.2},
			{Counter: counters.PortXmitDiscards, Weight: 20, Warn: 1, Crit: 50},
			{Counter: counters.OutOfSequence, Weight: 20, Warn: 1, Crit: 100},
			{Counter: counters.PacketSeqErr, Weight: 15, Warn: 0.5, Crit: 20},
			// ECN marks are how a healthy congestion-controlled fabric signals
			// load, so they carry a deliberately low weight.
			{Counter: counters.NpEcnMarkedRoceCounters, Weight: 10, Warn: 1000, Crit: 20000},
		},
		Alpha:          0.3,
		BaselineAlpha:  0.1,
		AnomalyZ:       3,
		AnomalyWarmup:  5,
		FlapCounter:    counters.LinkDowned,
		FlapWindow:     12,
		FlapPenalty:    30,
		SuspectBelow:   80,
		UnhealthyBelow: 50,
		RecoverSamples: 5,
	}
}

// LinkHealth is the scorer's verdict for one link at one point in time.
type LinkHealth struct {
	Node    string
	Link    string
	Ready   bool // false until a second sample allows rates to be computed
	Score   float64
	State   State
	Flaps   int                // link-down intervals inside the flap window
	Rates   map[string]float64 // smoothed per-second rate by counter
	Raw     map[string]uint64  // latest cumulative counters
	Reasons []string           // human-readable explanation of penalties
}

// NodeHealth aggregates the links of one node: the worst link decides.
type NodeHealth struct {
	Node      string
	Score     float64
	State     State
	WorstLink string
	Links     int
}

// Aggregate combines link verdicts into a node verdict.
func Aggregate(node string, links []LinkHealth) NodeHealth {
	n := NodeHealth{Node: node, Score: 100, State: Healthy, Links: len(links)}
	for _, l := range links {
		if l.Score < n.Score || n.WorstLink == "" {
			n.Score = l.Score
			n.WorstLink = l.Link
		}
		if l.State > n.State {
			n.State = l.State
		}
	}
	return n
}

type stat struct {
	n    int
	ewma float64 // fast, used for threshold checks
	mean float64 // slow baseline, used for z-score
	varr float64
}

type linkState struct {
	prev      counters.Sample
	havePrev  bool
	stats     map[string]*stat
	flapHist  []bool
	state     State
	betterRun int
	betterMax State
	last      LinkHealth
}

// Scorer keeps per-link history and scores incoming samples.
type Scorer struct {
	cfg   Config
	links map[string]*linkState
}

// New returns a Scorer using cfg.
func New(cfg Config) *Scorer {
	return &Scorer{cfg: cfg, links: make(map[string]*linkState)}
}

// Observe ingests one sample and returns the link's updated health.
func (s *Scorer) Observe(cur counters.Sample) LinkHealth {
	key := cur.Node + "|" + cur.Link
	ls, ok := s.links[key]
	if !ok {
		ls = &linkState{stats: make(map[string]*stat)}
		s.links[key] = ls
	}

	base := LinkHealth{
		Node:  cur.Node,
		Link:  cur.Link,
		Score: 100,
		State: ls.state,
		Rates: map[string]float64{},
		Raw:   copyValues(cur.Values),
	}

	if !ls.havePrev {
		ls.prev, ls.havePrev, ls.last = cur, true, base
		return base
	}
	dt := cur.Time.Sub(ls.prev.Time).Seconds()
	if dt <= 0 {
		return ls.last
	}

	h := base
	h.Ready = true
	var penalty float64

	for _, r := range s.cfg.Rules {
		cv, okc := cur.Values[r.Counter]
		pv, okp := ls.prev.Values[r.Counter]
		if !okc || !okp || cv < pv { // missing counter, or reset: skip this interval
			continue
		}
		rate := float64(cv-pv) / dt
		st := ls.stats[r.Counter]
		if st == nil {
			st = &stat{}
			ls.stats[r.Counter] = st
		}
		z := st.update(rate, s.cfg.Alpha, s.cfg.BaselineAlpha, math.Max(r.Warn*0.1, 1e-6))
		h.Rates[r.Counter] = st.ewma

		if p := penaltyFor(r, st.ewma); p > 0 {
			penalty += p
			h.Reasons = append(h.Reasons, fmt.Sprintf(
				"%s at %.2f/s (warn %.2f, crit %.2f): -%.1f", r.Counter, st.ewma, r.Warn, r.Crit, p))
		}
		if st.n > s.cfg.AnomalyWarmup && z >= s.cfg.AnomalyZ && rate > r.Warn {
			h.Reasons = append(h.Reasons, fmt.Sprintf(
				"anomaly: %s spiked to %.2f/s (z=%.1f)", r.Counter, rate, z))
		}
	}

	// Hard failure: count link-down events inside the sliding window.
	if cv, okc := cur.Values[s.cfg.FlapCounter]; okc {
		if pv, okp := ls.prev.Values[s.cfg.FlapCounter]; okp && cv >= pv {
			ls.flapHist = append(ls.flapHist, cv > pv)
			if len(ls.flapHist) > s.cfg.FlapWindow {
				ls.flapHist = ls.flapHist[len(ls.flapHist)-s.cfg.FlapWindow:]
			}
		}
	}
	for _, f := range ls.flapHist {
		if f {
			h.Flaps++
		}
	}
	if h.Flaps > 0 {
		p := float64(h.Flaps) * s.cfg.FlapPenalty
		penalty += p
		h.Reasons = append(h.Reasons, fmt.Sprintf(
			"%s flapped in %d of last %d intervals: -%.1f", s.cfg.FlapCounter, h.Flaps, len(ls.flapHist), p))
	}

	h.Score = math.Max(0, 100-penalty)
	h.State = ls.applyHysteresis(s.classify(h.Score), s.cfg.RecoverSamples)

	ls.prev, ls.last = cur, h
	return h
}

func (s *Scorer) classify(score float64) State {
	switch {
	case score < s.cfg.UnhealthyBelow:
		return Unhealthy
	case score < s.cfg.SuspectBelow:
		return Suspect
	}
	return Healthy
}

// applyHysteresis worsens immediately but improves only after `need`
// consecutive better samples, and then only to the worst raw state in that run.
func (ls *linkState) applyHysteresis(raw State, need int) State {
	switch {
	case raw > ls.state:
		ls.state, ls.betterRun = raw, 0
	case raw < ls.state:
		if ls.betterRun == 0 || raw > ls.betterMax {
			ls.betterMax = raw
		}
		ls.betterRun++
		if ls.betterRun >= need {
			ls.state, ls.betterRun = ls.betterMax, 0
		}
	default:
		ls.betterRun = 0
	}
	return ls.state
}

func penaltyFor(r Rule, rate float64) float64 {
	if rate <= r.Warn {
		return 0
	}
	if r.Crit <= r.Warn {
		return r.Weight
	}
	f := (rate - r.Warn) / (r.Crit - r.Warn)
	if f > 1 {
		f = 1
	}
	return r.Weight * f
}

// update folds a new per-second rate into the statistics and returns the
// z-score of that rate against the baseline as it stood before the update.
func (st *stat) update(rate, alpha, beta, sigmaFloor float64) float64 {
	if st.n == 0 {
		st.ewma, st.mean, st.n = rate, rate, 1
		return 0
	}
	diff := rate - st.mean
	z := diff / math.Max(math.Sqrt(st.varr), sigmaFloor)
	st.ewma = alpha*rate + (1-alpha)*st.ewma
	st.mean += beta * diff
	st.varr = (1 - beta) * (st.varr + beta*diff*diff)
	st.n++
	return z
}

func copyValues(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
