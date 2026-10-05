# Scoring model

Every link starts at 100. Penalties are subtracted and the result is clamped to
0. A node's score is the score of its worst link.

## Two failure shapes

AI training fabrics fail in two different ways, and they need different
detection.

**Hard failures** (the link drops). Counting cumulative `link_downed` deltas is
not enough on its own, because one blip and a flapping optic look alike in a
single interval. The scorer keeps a sliding window of the last `FlapWindow`
sample intervals and counts how many contained a link-down. Each one costs
`FlapPenalty` (30) points, so one flap makes a link Suspect and three make it
Unhealthy.

**Slow degradation** (the link stays up but gets worse). A link with a rising
symbol-error or discard rate keeps passing "is it up?" checks while quietly
costing collective-communication throughput. For these, each counter's
per-second rate is smoothed with an EWMA and compared to a per-counter rule:

| Counter | Weight | Warn (/s) | Crit (/s) | Why |
|---|---|---|---|---|
| `symbol_error` | 25 | 0.5 | 10 | Physical-layer errors: optics, cable, signal integrity |
| `port_rcv_errors` | 25 | 0.1 | 5 | Corrupted frames reaching the NIC |
| `link_error_recovery` | 20 | 0 | 0.2 | The link retrained itself: a precursor to a flap |
| `port_xmit_discards` | 20 | 1 | 50 | The NIC dropping its own egress traffic |
| `out_of_sequence` | 20 | 1 | 100 | RoCE retransmit trigger: packet loss or reordering |
| `packet_seq_err` | 15 | 0.5 | 20 | RoCE sequence errors: loss visible to the transport |
| `np_ecn_marked_roce_packets` | 10 | 1000 | 20000 | Congestion signal (see below) |

The penalty rises linearly from 0 at `Warn` to the full `Weight` at `Crit`.

## Congestion is not a fault

ECN marks are how a healthy, congestion-controlled RoCE fabric asks senders to
slow down. A link with heavy ECN marking and zero errors is busy, not broken.
It has a deliberately low weight, and the `congested` simulator scenario exists
to prove that congestion alone never causes a quarantine recommendation. Sustained
congestion is still worth alerting on, but through a separate path (fabric load
and placement), not through node health.

## Anomaly annotations

Alongside the thresholds, each counter keeps a slow baseline mean and variance.
When a sample's rate is at least `AnomalyZ` (3) standard deviations above that
baseline and above the counter's warn level, the verdict gets an `anomaly:`
reason. This is informational: it explains *why* a score dropped suddenly, but
does not change the score by itself.

## Hysteresis

A link gets worse immediately. It only improves after `RecoverSamples` (5)
consecutive better samples, and then only to the worst state seen in that run.
This stops a marginal link from bouncing between Healthy and Suspect, which
matters once a controller starts acting on these states.

## Counter resets

If a cumulative counter goes backwards (driver reload, firmware reset), that
interval is skipped for that counter instead of being scored as a huge burst.

## Tuning

`scorer.DefaultConfig()` holds starting values only. Real thresholds depend on
the NIC model, link speed, and workload, and should be calibrated against
fleet baselines before any automated action is tied to them.
