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
	dropPoliced
	dropReasons
)

var dropReasonNames = [dropReasons]string{
	dropUnexpectedSource: "unexpected_source",
	dropNoRemote:         "no_remote",
	dropNoDestination:    "no_destination",
	dropDirection:        "direction",
	dropMalformed:        "malformed",
	dropSendError:        "send_error",
	dropPoliced:          "policed",
}

type Metrics struct {
	relays  *prometheus.GaugeVec
	relayed *prometheus.CounterVec
	dropped *prometheus.CounterVec
	lost    *prometheus.CounterVec

	mu         sync.Mutex
	trunks     map[string]*trunkMetrics
	configured map[string]bool
}

type legMetrics struct {
	rtp, rtcp prometheus.Counter
	dropped   [dropReasons]prometheus.Counter
	lost      prometheus.Counter
}

type trunkMetrics struct {
	relays prometheus.Gauge
	legs   [legs]legMetrics
	open   int
}

func NewMetrics() *Metrics {
	return &Metrics{
		relays: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellaims_trunk_media_relays",
			Help: "Media streams that the media relay of SIP trunks relays now, by trunk ID.",
		}, []string{"trunk"}),
		relayed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trunk_media_packets_relayed_total",
			Help: "RTP and RTCP packets that the media relay of SIP trunks relayed, by trunk ID, by the leg they arrived on (phone or trunk) " +
				"and by protocol.",
		}, []string{"trunk", "leg", "protocol"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trunk_media_packets_dropped_total",
			Help: "RTP and RTCP packets that the media relay of SIP trunks dropped, by trunk ID, by the leg they arrived on and by reason: " +
				"unexpected_source (not from the remote address of the leg), no_remote (the leg has no remote " +
				"address yet), no_destination (the other leg has no remote address yet), direction (on hold or " +
				"inactive), malformed, send_error, and policed (above the bandwidth negotiated for the stream).",
		}, []string{"trunk", "leg", "reason"}),
		lost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trunk_media_packets_lost_total",
			Help: "RTP packets lost before reaching the media relay of SIP trunks, from gaps in RTP sequence numbers, by trunk ID and by " +
				"the leg they were sent on.",
		}, []string{"trunk", "leg"}),
		trunks: map[string]*trunkMetrics{},
	}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.relays, m.relayed, m.dropped, m.lost}
}

func (m *Metrics) Configure(trunks []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.configured = make(map[string]bool, len(trunks))

	for _, id := range trunks {
		m.configured[id] = true
		m.trunkLocked(id)
	}

	for id, t := range m.trunks {
		if !m.configured[id] && t.open == 0 {
			m.forgetLocked(id)
		}
	}
}

func (m *Metrics) open(name string) *trunkMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.trunkLocked(name)
	t.open++
	t.relays.Inc()

	return t
}

func (m *Metrics) close(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.trunks[name]
	if t == nil {
		return
	}

	t.open--
	t.relays.Dec()

	if t.open == 0 && m.configured != nil && !m.configured[name] {
		m.forgetLocked(name)
	}
}

func (m *Metrics) trunk(name string) *trunkMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.trunkLocked(name)
}

func (m *Metrics) trunkLocked(name string) *trunkMetrics {
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

	m.forgetLocked(trunk)
}

func (m *Metrics) forgetLocked(trunk string) {
	delete(m.trunks, trunk)

	labels := prometheus.Labels{"trunk": trunk}
	m.relays.DeletePartialMatch(labels)
	m.relayed.DeletePartialMatch(labels)
	m.dropped.DeletePartialMatch(labels)
	m.lost.DeletePartialMatch(labels)
}
