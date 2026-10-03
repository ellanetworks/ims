//go:build linux && (amd64 || arm64)

package integration

import (
	"flag"
	"fmt"
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

var callMethods = []string{"INVITE", "ACK", "PRACK", "UPDATE", "BYE", "CANCEL"}

type recorder struct {
	mu    sync.Mutex
	dir   string
	n     int
	index []string
}

func (s *scene) record(flow string) {
	s.t.Helper()

	if *corpusDir == "" {
		return
	}

	s.rec.quiesce()

	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()

	if s.rec.dir != "" {
		index := "# file\tframe\ttransport\tsrc\tdst\tesp\tgtp\tbytes\n" + strings.Join(s.rec.index, "")
		if err := os.WriteFile(filepath.Join(s.rec.dir, "index.tsv"), []byte(index), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}

	s.rec.dir, s.rec.n, s.rec.index = "", 0, nil

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

	s.rec.dir = dir
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

func (s *scene) trace(m sip.Message, sent bool) {
	cseq, err := m.Env().Header.CSeq()
	if err != nil || !slices.Contains(callMethods, cseq.Method) {
		return
	}

	raw := m.Bytes()

	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()

	if s.rec.dir == "" {
		return
	}

	s.rec.n++

	name := fmt.Sprintf("%03d-%s.sip", s.rec.n, cseq.Method)
	if res, ok := m.(*sip.Response); ok {
		name = fmt.Sprintf("%03d-%d-%s.sip", s.rec.n, res.StatusCode, cseq.Method)
	}

	if err := os.WriteFile(filepath.Join(s.rec.dir, name), raw, 0o644); err != nil {
		s.t.Errorf("write %s: %v", name, err)
		return
	}

	f := m.Env().Flow

	src, dst := f.Remote, f.Local
	if sent {
		src, dst = f.Local, f.Remote
	}

	s.rec.index = append(s.rec.index, strings.Join([]string{
		name, strconv.Itoa(s.rec.n), string(f.Transport), src.String(), dst.String(), "esp", "-", strconv.Itoa(len(raw)),
	}, "\t")+"\n")
}
