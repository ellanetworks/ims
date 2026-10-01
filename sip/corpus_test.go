package sip_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip/internal/corpus"
)

func TestCorpusRoundTrip(t *testing.T) {
	corpus.RunCorpus(t, corpus.CheckRoundTrip)
}

func TestCorpusURIs(t *testing.T) {
	corpus.RunCorpus(t, corpus.CheckURIs)
}

func TestTorture(t *testing.T) {
	corpus.RunTorture(t)
}
