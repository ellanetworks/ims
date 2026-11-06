package trgw

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	protocolRTP  = "rtp"
	protocolRTCP = "rtcp"
)

type dropReason int

const (
	dropUnexpectedSource dropReason = iota
	dropNoRemote
	dropNoDestination
	dropDirection
	dropMalformed
	dropSendError
	dropReasons
)

var dropReasonNames = [dropReasons]string{
	dropUnexpectedSource: "unexpected_source",
	dropNoRemote:         "no_remote",
	dropNoDestination:    "no_destination",
	dropDirection:        "direction",
	dropMalformed:        "malformed",
	dropSendError:        "send_error",
}

type Metrics struct {
	relays  *prometheus.GaugeVec
	relayed *prometheus.CounterVec
	dropped *prometheus.CounterVec
	lost    *prometheus.CounterVec

	mu     sync.Mutex
	trunks map[string]*trunkMetrics
}

type legMetrics struct {
	rtp, rtcp prometheus.Counter
	dropped   [dropReasons]prometheus.Counter
	lost      prometheus.Counter
}

type trunkMetrics struct {
	relays prometheus.Gauge
	legs   [legs]legMetrics
}

func NewMetrics() *Metrics {
	return &Metrics{
		relays: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellaims_trgw_relays",
			Help: "Media relays the TrGW holds open, by trunk ID.",
		}, []string{"trunk"}),
		relayed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trgw_packets_relayed_total",
			Help: "RTP and RTCP packets the TrGW relayed, by trunk ID, by the leg they arrived on (phone or trunk) " +
				"and by protocol.",
		}, []string{"trunk", "leg", "protocol"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trgw_packets_dropped_total",
			Help: "RTP and RTCP packets the TrGW dropped, by trunk ID, by the leg they arrived on and by reason: " +
				"unexpected_source (not from the remote address of the leg), no_remote (the leg has no remote " +
				"address yet), no_destination (the other leg has no remote address yet), direction (on hold or " +
				"inactive), malformed, and send_error.",
		}, []string{"trunk", "leg", "reason"}),
		lost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trgw_packets_lost_total",
			Help: "RTP packets lost before reaching the TrGW, from gaps in RTP sequence numbers, by trunk ID and by " +
				"the leg they were sent on.",
		}, []string{"trunk", "leg"}),
		trunks: map[string]*trunkMetrics{},
	}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.relays, m.relayed, m.dropped, m.lost}
}

func (m *Metrics) trunk(name string) *trunkMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t := m.trunks[name]; t != nil {
		return t
	}

	t := &trunkMetrics{relays: m.relays.WithLabelValues(name)}

	for i := range t.legs {
		l := &t.legs[i]
		leg := legNames[i]
		l.rtp = m.relayed.WithLabelValues(name, leg, protocolRTP)
		l.rtcp = m.relayed.WithLabelValues(name, leg, protocolRTCP)
		l.lost = m.lost.WithLabelValues(name, leg)

		for r := range l.dropped {
			l.dropped[r] = m.dropped.WithLabelValues(name, leg, dropReasonNames[r])
		}
	}

	m.trunks[name] = t

	return t
}

func (m *Metrics) Forget(trunk string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.trunks, trunk)

	labels := prometheus.Labels{"trunk": trunk}
	m.relays.DeletePartialMatch(labels)
	m.relayed.DeletePartialMatch(labels)
	m.dropped.DeletePartialMatch(labels)
	m.lost.DeletePartialMatch(labels)
}
