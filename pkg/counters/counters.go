// Package counters defines the telemetry sample model and the sources that
// produce it: a sysfs reader for real RDMA/Ethernet NICs and a deterministic
// simulator for development on machines without RDMA hardware.
package counters

import (
	"context"
	"time"
)

// Counter names. These match the Linux RDMA sysfs names under
// /sys/class/infiniband/<dev>/ports/<port>/{counters,hw_counters}.
const (
	LinkDowned              = "link_downed"
	LinkErrorRecovery       = "link_error_recovery"
	SymbolError             = "symbol_error"
	PortRcvErrors           = "port_rcv_errors"
	PortXmitDiscards        = "port_xmit_discards"
	PortRcvData             = "port_rcv_data"
	PortXmitData            = "port_xmit_data"
	OutOfSequence           = "out_of_sequence"
	PacketSeqErr            = "packet_seq_err"
	NpEcnMarkedRoceCounters = "np_ecn_marked_roce_packets"
)

// Sample is one reading of every cumulative counter on a single link.
type Sample struct {
	Time   time.Time
	Node   string
	Link   string // "<device>/<port>", e.g. "mlx5_0/1"
	Values map[string]uint64
}

// Source produces one Sample per link each time Collect is called.
type Source interface {
	Collect(ctx context.Context) ([]Sample, error)
}
