package counters

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// Scenario selects the behaviour a simulated link exhibits.
type Scenario string

const (
	// ScenarioHealthy: steady traffic, an occasional stray symbol error.
	ScenarioHealthy Scenario = "healthy"
	// ScenarioFlapping: the link goes down and recovers repeatedly (a hard
	// failure pattern, e.g. a bad optic or cable).
	ScenarioFlapping Scenario = "flapping"
	// ScenarioDegrading: error and discard rates ramp up steadily. Nothing
	// hard-fails, but throughput would silently suffer (the "slow NIC" case).
	ScenarioDegrading Scenario = "degrading"
	// ScenarioCongested: heavy ECN marking with no errors. This is normal
	// behaviour for a busy fabric and must NOT be treated as a fault.
	ScenarioCongested Scenario = "congested"
)

// LinkSpec names a simulated link and the scenario it follows.
type LinkSpec struct {
	Name     string
	Scenario Scenario
}

// ParseLinkSpecs parses "mlx5_0/1=healthy,mlx5_1/1=degrading".
func ParseLinkSpecs(s string) ([]LinkSpec, error) {
	var specs []LinkSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, sc, ok := strings.Cut(part, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid link spec %q (want name=scenario)", part)
		}
		switch Scenario(sc) {
		case ScenarioHealthy, ScenarioFlapping, ScenarioDegrading, ScenarioCongested:
		default:
			return nil, fmt.Errorf("unknown scenario %q for link %q", sc, name)
		}
		specs = append(specs, LinkSpec{Name: name, Scenario: Scenario(sc)})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no links specified")
	}
	return specs, nil
}

var allCounters = []string{
	LinkDowned, LinkErrorRecovery, SymbolError, PortRcvErrors, PortXmitDiscards,
	PortRcvData, PortXmitData, OutOfSequence, PacketSeqErr, NpEcnMarkedRoceCounters,
}

type simLink struct {
	spec LinkSpec
	vals map[string]uint64
}

// Simulator is a deterministic (seeded) Source. Each Collect call advances
// simulated time by step, so tests and demos do not have to wait in real time.
type Simulator struct {
	node  string
	step  time.Duration
	now   time.Time
	tick  int
	rng   *rand.Rand
	links []*simLink
}

// NewSimulator builds a simulator whose clock starts at start.
func NewSimulator(node string, specs []LinkSpec, start time.Time, step time.Duration, seed int64) *Simulator {
	s := &Simulator{node: node, step: step, now: start, rng: rand.New(rand.NewSource(seed))}
	for _, sp := range specs {
		l := &simLink{spec: sp, vals: make(map[string]uint64, len(allCounters))}
		for _, c := range allCounters {
			l.vals[c] = 0
		}
		s.links = append(s.links, l)
	}
	return s
}

// Collect advances simulated time by one step and returns a Sample per link.
func (s *Simulator) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.tick++
	s.now = s.now.Add(s.step)
	out := make([]Sample, 0, len(s.links))
	for _, l := range s.links {
		s.advance(l)
		vals := make(map[string]uint64, len(l.vals))
		for k, v := range l.vals {
			vals[k] = v
		}
		out = append(out, Sample{Time: s.now, Node: s.node, Link: l.spec.Name, Values: vals})
	}
	return out, nil
}

func (s *Simulator) advance(l *simLink) {
	v := l.vals
	v[PortXmitData] += uint64(1_000_000 + s.rng.Intn(200_000))
	v[PortRcvData] += uint64(1_000_000 + s.rng.Intn(200_000))

	switch l.spec.Scenario {
	case ScenarioHealthy:
		if s.rng.Float64() < 0.02 {
			v[SymbolError]++
		}
	case ScenarioFlapping:
		if s.tick >= 3 && s.tick%4 == 0 {
			v[LinkDowned]++
			v[LinkErrorRecovery]++
			v[SymbolError] += 2
		}
	case ScenarioDegrading:
		if s.tick > 5 {
			n := uint64(s.tick - 5)
			v[SymbolError] += 8*n + uint64(s.rng.Intn(4))
			v[PortRcvErrors] += 2 * n
			v[PortXmitDiscards] += 3 * n
		}
	case ScenarioCongested:
		v[NpEcnMarkedRoceCounters] += uint64(8000 + s.rng.Intn(4000))
		v[OutOfSequence] += uint64(s.rng.Intn(3))
	}
}
