# fabric-sentinel

![CI](https://github.com/YOUR_GITHUB_USERNAME/fabric-sentinel/actions/workflows/ci.yml/badge.svg)

Network health scoring for GPU-cluster NICs and links, with the end goal of automatically quarantining degraded nodes before they hurt large training jobs.

On a large training cluster, one degraded NIC, cable, or link can stall collectives for thousands of GPUs while every "is it up?" check still passes. `fabric-sentinel` watches per-link counters, separates **hard failures** (links flapping) from **slow degradation** (error rates creeping up), and turns both into a 0-100 health score and a `Healthy / Suspect / Unhealthy` state per link and node. It deliberately does **not** treat congestion as a fault.

> **Status: prototype, milestone 1 of 3.** The per-node agent and scoring engine are done and tested. The Kubernetes controller that acts on the scores is next. See [Roadmap](#roadmap) and [Limitations](#limitations).

## How it works

```
 /sys/class/infiniband          ┌────────────────────────── agent (one per node) ──────────────────────────┐
 (RDMA / RoCE counters)  ──►    │  counters.Source ──► scorer ──► exporter ──► /metrics  (Prometheus)       │
        or                      │  (sysfs | simulator)   │  ▲                  /status   (JSON verdicts)      │
 simulator (no hardware)  ──►   │                        │  └ per-link state:  /healthz  (liveness)          │
                                └────────────────────────┴─────────────────────────────────────────────────┘
                                   EWMA rates · flap window · z-score annotations · hysteresis
```

- **`pkg/counters`**: reads cumulative counters from the Linux RDMA sysfs tree, or generates deterministic synthetic ones (`healthy`, `degrading`, `flapping`, `congested`) so everything runs without RDMA hardware.
- **`pkg/scorer`**: the scoring engine. Details in [docs/scoring-model.md](docs/scoring-model.md).
- **`pkg/metrics`**: a small Prometheus text-format writer. The agent has **zero third-party dependencies**.
- **`cmd/agent`**: the node agent exposing `/metrics`, `/status` and `/healthz`.

## Quickstart

Requires Go 1.22+. No other dependencies.

```sh
make check      # gofmt check, go vet, tests with the race detector
make demo       # start the agent on 4 simulated links and print what it sees
```

Or run it yourself and look around:

```sh
make run-sim                              # in one terminal
curl -s localhost:9101/status             # JSON verdicts with human-readable reasons
curl -s localhost:9101/metrics | grep fabric_sentinel_link_state
```

### Example: what the demo shows

Four simulated links, each following a different scenario. These are real state-change log lines from the agent (trimmed):

```
agent: link mlx5_0/1 -> Healthy   (score 100.0)                                  # steady traffic
agent: link mlx5_3/1 -> Healthy   (score 99.6)  np_ecn_marked_roce_packets ...   # congested, but no errors: NOT a fault
agent: link mlx5_2/1 -> Suspect   (score 64.0)  link_downed flapped in 1 of last 3 intervals: -30.0
agent: link mlx5_2/1 -> Unhealthy (score 32.6)  link_downed flapped in 2 of last 7 intervals: -60.0
agent: link mlx5_1/1 -> Suspect   (score 75.9)  symbol_error at 6.59/s (warn 0.50, crit 10.00): -16.0 ...
agent: link mlx5_1/1 -> Unhealthy (score 49.2)  symbol_error at 19.10/s ... port_rcv_errors at 4.67/s ...
```

and the matching Prometheus series:

```
fabric_sentinel_node_health_score{node="demo-node"} 4.47
fabric_sentinel_link_health_score{link="mlx5_0/1",node="demo-node"} 100
fabric_sentinel_link_health_score{link="mlx5_1/1",node="demo-node"} 46.08
fabric_sentinel_link_health_score{link="mlx5_2/1",node="demo-node"} 4.47
fabric_sentinel_link_state{link="mlx5_1/1",node="demo-node",state="Unhealthy"} 1
```

### Reading real counters

```sh
./bin/agent -mode sysfs -sysfs-root /sys/class/infiniband
```

Flags: `-mode sysfs|sim`, `-listen :9101`, `-interval 5s`, `-node <name>`; simulator only: `-sim-links`, `-sim-step`, `-sim-seed`.

## Metrics

| Metric | Type | Meaning |
|---|---|---|
| `fabric_sentinel_link_health_score` | gauge | 0-100 per link |
| `fabric_sentinel_link_state` | gauge | `state` label; 1 for the active state |
| `fabric_sentinel_link_flaps` | gauge | link-down intervals in the flap window |
| `fabric_sentinel_link_counter_rate` | gauge | smoothed per-second rate of each scored counter |
| `fabric_sentinel_link_counter_total` | counter | raw cumulative NIC/link counters |
| `fabric_sentinel_node_health_score` / `_node_state` | gauge | worst link decides |
| `fabric_sentinel_collect_errors_total` | counter | failed collection cycles |

## Roadmap

- [x] **Milestone 1: agent and scoring engine.** Counter sources (sysfs + simulator), EWMA/flap/z-score scoring, hysteresis, Prometheus + JSON endpoints, CI.
- [ ] **Milestone 2: Kubernetes controller.** `NodeNetworkHealth` CRD (`Healthy -> Suspect -> Quarantined`); cordon and drain with PodDisruptionBudget respect; safety rails (max-percent quarantined, per-rack blast radius, dry-run, manual override, audit Events); tested on `kind`.
- [ ] **Milestone 3: qualification and fault injection.** A node-admission gate based on an RDMA bandwidth test (`perftest`), `tc netem` fault injection to prove detect -> quarantine -> recover, a Grafana dashboard, a runbook, and an example RCA write-up.

## Limitations

This is a prototype, and it is worth being clear about what it has and has not been shown to do.

- **Not yet validated on real RDMA hardware.** Sysfs mode is unit-tested against a fake sysfs tree and the scenarios above are simulated. Counter availability and semantics vary by NIC and driver (for example, some report `N/A`, which is skipped).
- **Thresholds are starting points.** The defaults in `scorer.DefaultConfig()` are reasoned, not calibrated against a real fleet. In production they would be tuned per NIC model and link speed from baseline data.
- **Per-node only for now.** There is no fleet-level view, topology awareness (rack/row/plane), or workload correlation yet; the controller milestone adds the first of those.
- **No switch-side telemetry.** A link problem visible only from the switch (gNMI/SONiC counters) is out of scope for this version.

## Project layout

```
cmd/agent/        node agent: HTTP exporter and main loop
pkg/counters/     Source interface, sysfs reader, deterministic simulator
pkg/scorer/       scoring engine (EWMA, flap window, z-score, hysteresis)
pkg/metrics/      dependency-free Prometheus text-format writer
hack/demo.sh      scripted end-to-end demo
docs/             scoring model write-up
.github/workflows CI: gofmt, vet, race tests, build
```

## License

MIT
