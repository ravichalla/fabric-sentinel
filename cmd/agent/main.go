// Command agent scores NIC/link health on one node and exposes the result as
// Prometheus metrics (/metrics), JSON (/status) and a liveness check (/healthz).
//
// It runs in two modes:
//
//	sysfs  read real counters from /sys/class/infiniband (RDMA/RoCE NICs)
//	sim    generate deterministic synthetic counters (no hardware needed)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fabric-sentinel/pkg/counters"
	"fabric-sentinel/pkg/scorer"
)

func main() {
	var (
		mode      = flag.String("mode", "sysfs", "counter source: sysfs or sim")
		listen    = flag.String("listen", ":9101", "address for /metrics, /status and /healthz")
		node      = flag.String("node", "", "node name used in labels (default: hostname)")
		interval  = flag.Duration("interval", 5*time.Second, "collection interval")
		sysfsRoot = flag.String("sysfs-root", counters.DefaultSysfsRoot, "RDMA sysfs root (sysfs mode)")
		simLinks  = flag.String("sim-links",
			"mlx5_0/1=healthy,mlx5_1/1=degrading,mlx5_2/1=flapping,mlx5_3/1=congested",
			"simulated links as name=scenario,... (sim mode)")
		simStep = flag.Duration("sim-step", 5*time.Second, "simulated time per tick (sim mode)")
		simSeed = flag.Int64("sim-seed", 1, "random seed (sim mode)")
	)
	flag.Parse()

	if *node == "" {
		h, err := os.Hostname()
		if err != nil {
			h = "unknown"
		}
		*node = h
	}

	src, err := newSource(*mode, *node, *sysfsRoot, *simLinks, *simStep, *simSeed)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exp := newExporter(*node, 3**interval)
	srv := &http.Server{Addr: *listen, Handler: exp.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("agent: node=%s mode=%s listening on %s", *node, *mode, *listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("agent: http server: %v", err)
		}
	}()

	sc := scorer.New(scorer.DefaultConfig())
	lastStates := map[string]scorer.State{}

	collect := func() {
		samples, err := src.Collect(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("agent: collect: %v", err)
				exp.fail(err)
			}
			return
		}
		links := make([]scorer.LinkHealth, 0, len(samples))
		for _, s := range samples {
			h := sc.Observe(s)
			if prev, seen := lastStates[h.Link]; h.Ready && (!seen || prev != h.State) {
				log.Printf("agent: link %s -> %s (score %.1f) %v", h.Link, h.State, h.Score, h.Reasons)
			}
			if h.Ready {
				lastStates[h.Link] = h.State
			}
			links = append(links, h)
		}
		exp.publish(links)
	}

	collect()
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			log.Print("agent: stopped")
			return
		case <-tick.C:
			collect()
		}
	}
}

func newSource(mode, node, sysfsRoot, simLinks string, simStep time.Duration, seed int64) (counters.Source, error) {
	switch mode {
	case "sysfs":
		return &counters.Sysfs{Root: sysfsRoot, Node: node}, nil
	case "sim":
		specs, err := counters.ParseLinkSpecs(simLinks)
		if err != nil {
			return nil, err
		}
		return counters.NewSimulator(node, specs, time.Now(), simStep, seed), nil
	}
	return nil, fmt.Errorf("unknown mode %q (want sysfs or sim)", mode)
}
