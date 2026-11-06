package trgw

import "math/bits"

const (
	maxDropout    = 3000
	maxMisorder   = 100
	minSequential = 2
	seqMod        = 1 << 16
	lossWindow    = 64
)

// RFC 3550 section A.1
type lossCounter struct {
	started   bool
	ssrc      uint32
	probation int
	maxSeq    uint16
	badSeq    uint32
	window    uint64
}

func (c *lossCounter) restart(seq uint16) {
	c.maxSeq = seq
	c.badSeq = seqMod + 1
	c.window = ^uint64(0)
}

// RFC 3550 section A.1
func (c *lossCounter) update(ssrc uint32, seq uint16) uint64 {
	if !c.started || ssrc != c.ssrc {
		*c = lossCounter{started: true, ssrc: ssrc, maxSeq: seq - 1, probation: minSequential}
	}

	if c.probation > 0 {
		if seq != c.maxSeq+1 {
			c.probation = minSequential - 1
			c.maxSeq = seq

			return 0
		}

		c.probation--
		c.maxSeq = seq

		if c.probation == 0 {
			c.restart(seq)
		}

		return 0
	}

	udelta := seq - c.maxSeq

	switch {
	case udelta < maxDropout:
		c.maxSeq = seq
		return c.advance(uint64(udelta))
	case uint32(udelta) <= seqMod-maxMisorder:
		if uint32(seq) != c.badSeq {
			c.badSeq = (uint32(seq) + 1) & (seqMod - 1)
			return 0
		}

		c.restart(seq)

		return 0
	}

	if behind := uint64(seqMod - uint32(udelta)); behind < lossWindow {
		c.window |= 1 << behind
	}

	return 0
}

func (c *lossCounter) advance(d uint64) uint64 {
	if d == 0 {
		return 0
	}

	if d >= lossWindow {
		lost := uint64(bits.OnesCount64(^c.window)) + d - lossWindow
		c.window = 1

		return lost
	}

	leaving := ^c.window >> (lossWindow - d)
	c.window = c.window<<d | 1

	return uint64(bits.OnesCount64(leaving))
}
