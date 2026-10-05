package counters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultSysfsRoot is where the Linux RDMA subsystem exposes devices.
const DefaultSysfsRoot = "/sys/class/infiniband"

// Sysfs reads cumulative counters from the Linux RDMA sysfs tree. It works
// for InfiniBand and RoCE devices alike (for example mlx5_* on ConnectX NICs).
type Sysfs struct {
	Root string
	Node string
	Now  func() time.Time // defaults to time.Now
}

// Collect walks <root>/<dev>/ports/<port>/{counters,hw_counters} and returns
// one Sample per port. Counters that cannot be read or parsed (some drivers
// report "N/A") are skipped rather than failing the whole collection.
func (s *Sysfs) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := s.Root
	if root == "" {
		root = DefaultSysfsRoot
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}

	devs, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}

	var out []Sample
	for _, dev := range devs {
		portsDir := filepath.Join(root, dev.Name(), "ports")
		ports, err := os.ReadDir(portsDir)
		if err != nil {
			continue
		}
		for _, port := range ports {
			base := filepath.Join(portsDir, port.Name())
			vals := map[string]uint64{}
			readCounterDir(filepath.Join(base, "counters"), vals)
			readCounterDir(filepath.Join(base, "hw_counters"), vals)
			if len(vals) == 0 {
				continue
			}
			out = append(out, Sample{
				Time:   now(),
				Node:   s.Node,
				Link:   dev.Name() + "/" + port.Name(),
				Values: vals,
			})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no RDMA ports with readable counters found under " + root)
	}
	return out, nil
}

func readCounterDir(dir string, into map[string]uint64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			continue
		}
		into[e.Name()] = v
	}
}
