//go:build linux && (amd64 || arm64)

package integration

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
)

var corpusDir = flag.String("corpus", "", "write the call messages the test UEs exchange into this directory, one subdirectory per flow")

var callMethods = []string{"INVITE", "ACK", "PRACK", "UPDATE", "BYE", "CANCEL", "MESSAGE"}

type wire struct {
	ue        int
	sent      bool
	method    string
	status    int
	transport sip.Transport
	src, dst  netip.AddrPort
	size      int
}

type wireLog struct {
	mu      sync.Mutex
	entries []wire
}

func (w *wireLog) add(e wire) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.entries = append(w.entries, e)
}

func (w *wireLog) find(match func(wire) bool) []wire {
	w.mu.Lock()
	defer w.mu.Unlock()

	var out []wire

	for _, e := range w.entries {
		if match(e) {
			out = append(out, e)
		}
	}

	return out
}

type recorder struct {
	mu    sync.Mutex
	dir   string
	n     int
	index []string
}

func (s *scene) tracer(ue int, plain bool) func(sip.Message, sip.Flow, bool) {
	protection := "esp"
	if plain {
		protection = "plain"
	}

	return func(m sip.Message, f sip.Flow, sent bool) {
		cseq, err := m.Env().Header.CSeq()
		if err != nil || !slices.Contains(callMethods, cseq.Method) {
			return
		}

		raw := m.Bytes()

		src, dst := f.Remote, f.Local
		if sent {
			src, dst = f.Local, f.Remote
		}

		e := wire{ue: ue, sent: sent, method: cseq.Method, transport: f.Transport, src: src, dst: dst, size: len(raw)}
		if res, ok := m.(*sip.Response); ok {
			e.status = res.StatusCode
		}

		s.wire.add(e)
		s.rec.write(e, raw, protection)
	}
}

func (s *scene) record(flow string) {
	s.t.Helper()

	if *corpusDir == "" {
		return
	}

	s.rec.quiesce()

	if err := s.rec.flush(); err != nil {
		s.t.Fatal(err)
	}

	if flow == "" {
		return
	}

	dir := filepath.Join(*corpusDir, flow)
	if err := os.RemoveAll(dir); err != nil {
		s.t.Fatal(err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}

	s.rec.mu.Lock()
	s.rec.dir = dir
	s.rec.mu.Unlock()
}

func (r *recorder) flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dir != "" {
		index := "# file\tframe\ttransport\tsrc\tdst\tesp\tgtp\tbytes\n" + strings.Join(r.index, "")
		if err := os.WriteFile(filepath.Join(r.dir, "index.tsv"), []byte(index), 0o644); err != nil {
			return err
		}
	}

	r.dir, r.n, r.index = "", 0, nil

	return nil
}

func (r *recorder) quiesce() {
	r.mu.Lock()
	last := r.n
	r.mu.Unlock()

	for range 10 {
		time.Sleep(100 * time.Millisecond)

		r.mu.Lock()
		n := r.n
		r.mu.Unlock()

		if n == last {
			return
		}

		last = n
	}
}

func (r *recorder) write(e wire, raw []byte, protection string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dir == "" {
		return
	}

	r.n++

	name := fmt.Sprintf("%03d-%s.sip", r.n, e.method)
	if e.status != 0 {
		name = fmt.Sprintf("%03d-%d-%s.sip", r.n, e.status, e.method)
	}

	if err := os.WriteFile(filepath.Join(r.dir, name), raw, 0o644); err != nil {
		return
	}

	r.index = append(r.index, strings.Join([]string{
		name, strconv.Itoa(r.n), string(e.transport), e.src.String(), e.dst.String(), protection, "-", strconv.Itoa(e.size),
	}, "\t")+"\n")
}
