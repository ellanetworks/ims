//go:build linux && (amd64 || arm64)

// Package loadtest loads a running Ella IMS from another host: phones over IPsec, with a fake HSS over Cx and a fake
// PCRF over Rx that it configures the IMS with. It measures the numbers of the performance reference: the
// registrations per second, the memory per registered subscriber, and the calls per second.
//
// The UEs bind addresses of -ue-net, which the host takes as its own (AnyIP), and which the IMS host routes back:
//
//	load host: ip route add local 10.64.0.0/14 dev lo
//	IMS host:  ip route add 10.64.0.0/14 via <load host>
//
// Build it with go test -c ./internal/loadtest, and run it as root with -load.
package loadtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/pcrftest"
	"github.com/ellanetworks/ims/testue"
)

var (
	load = flag.Bool("load", false, "load the IMS at -ims")

	imsAddr   = flag.String("ims", "", "the address of the IMS: its P-CSCF on port 5060, its API on port 5020")
	localAddr = flag.String("local", "", "the address of this host, the fake HSS's and PCRF's")
	ueNet     = flag.String("ue-net", "10.64.0.0/14", "the addresses of the UEs, local to this host")

	subs     = flag.Int("subs", 1000, "the subscribers, each a UE that registers")
	regSteps = flag.String("reg-rates", "50,100,200", "the registrations per second the UEs register at, each for -reg-step, the last until all have")
	regStep  = flag.Duration("reg-step", 30*time.Second, "how long each registration rate lasts")

	callSteps = flag.String("cps", "", "the calls per second to measure, each for -call-step")
	callStep  = flag.Duration("call-step", 2*time.Minute, "how long each call rate lasts")
	hold      = flag.Duration("hold", 90*time.Second, "how long an answered call lasts")
	reregEach = flag.Duration("rereg", 3000*time.Second, "how often each registered UE re-registers during the calls, 0 for never")

	settle = flag.Duration("settle", 30*time.Second, "the wait before a measure of the memory")
)

const (
	domain = "ims.mnc001.mcc001.3gppnetwork.org"

	pcscfPort = 5060
	apiPort   = 5020
)

var (
	testK   = []byte("0123456789abcdef")
	testOPc = []byte("fedcba9876543210")
)

func TestLoad(t *testing.T) {
	if !*load {
		t.Skip("run with -load")
	}

	ims := netip.MustParseAddr(*imsAddr)
	local := netip.MustParseAddr(*localAddr)
	api := fmt.Sprintf("http://%s:%d/api/v1", ims, apiPort)

	quiet := slog.New(slog.DiscardHandler)

	hss := hsstest.New(t, hsstest.Config{Realm: domain, IMSHost: "ims." + domain, Address: local, IMSAddress: ims, Logger: quiet})
	pcrf := pcrftest.New(t, pcrftest.Config{
		Realm: "epc.mnc001.mcc001.3gppnetwork.org", IMSHost: "ims." + domain, IMSRealm: domain, Address: local, IMSAddress: ims,
		Logger: quiet,
	})

	// The fakes keep the requests they answer for the tests to look at: none here.
	go func() {
		for range hss.Requests() {
		}
	}()
	go func() {
		for range pcrf.Requests() {
		}
	}()

	configure(t, api, hss, pcrf)

	x, err := ipsec.Open()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = x.Close() })

	kernel := testue.XFRMOf(x)

	addr := netip.MustParsePrefix(*ueNet).Masked().Addr()
	ues := make([]*testue.UE, *subs)

	for i := range ues {
		addr = addr.Next()
		sub := subscriberAt(i)
		hss.Add(sub.hss())

		u, err := testue.New(testue.Config{
			IMSI: sub.imsi, IMEI: sub.imei, K: testK, OPc: testOPc,
			PCSCF: netip.AddrPortFrom(ims, pcscfPort), Local: addr, Kernel: kernel,
			MMTel:  testue.MMTelConfig{AcceptCalls: true},
			Logger: quiet,
		})
		if err != nil {
			t.Fatalf("UE %d: %v", i, err)
		}

		t.Cleanup(func() { _ = u.Close() })

		go answer(u)

		ues[i] = u
	}

	uesMu.Lock()
	uesAll = ues
	uesMu.Unlock()

	before := scrape(t, api)

	registered := registerAll(t, ues)

	time.Sleep(*settle)

	after := scrape(t, api)
	t.Logf("memory: %d registered, %.1f MiB resident (%.1f MiB before), %.1f KiB per subscriber, %d goroutines",
		int(after["ellaims_registered_subscribers"]), after["process_resident_memory_bytes"]/(1<<20),
		before["process_resident_memory_bytes"]/(1<<20),
		(after["process_resident_memory_bytes"]-before["process_resident_memory_bytes"])/1024/float64(max(1, len(registered))),
		int(after["go_goroutines"]))

	if *callSteps != "" {
		callAll(t, api, registered)
	}
}

// configure points the IMS at the fake HSS over Cx and the fake PCRF over Rx, in place of its peers.
func configure(t *testing.T, api string, hss *hsstest.HSS, pcrf *pcrftest.PCRF) {
	t.Helper()

	var peers struct {
		Result struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"result"`
	}

	call(t, http.MethodGet, api+"/diameter/peers", nil, &peers)

	// The policy over Rx needs a peer serving Rx: none while the peers change.
	call(t, http.MethodPut, api+"/policy", map[string]any{"interface": "none"}, nil)

	for _, p := range peers.Result.Items {
		call(t, http.MethodDelete, api+"/diameter/peers/"+p.ID, nil, nil)
	}

	for _, p := range []struct {
		host string
		addr netip.AddrPort
		app  string
	}{{hss.Host(), hss.Addr(), "cx"}, {pcrf.Host(), pcrf.Addr(), "rx"}} {
		call(t, http.MethodPost, api+"/diameter/peers", map[string]any{
			"host": p.host, "address": p.addr.Addr().String(), "port": p.addr.Port(), "applications": []string{p.app},
		}, nil)
	}

	call(t, http.MethodPut, api+"/diameter/routes/rx", map[string]any{"realm": pcrf.Realm()}, nil)
	call(t, http.MethodPut, api+"/policy", map[string]any{"interface": "rx"}, nil)

	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) {
		var st struct {
			Result struct {
				Items []struct {
					Status struct {
						State string `json:"state"`
					} `json:"status"`
				} `json:"items"`
			} `json:"result"`
		}

		call(t, http.MethodGet, api+"/diameter/peers", nil, &st)

		open := 0

		for _, p := range st.Result.Items {
			if p.Status.State == "open" {
				open++
			}
		}

		if open == 2 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the IMS's Diameter peers did not open: %+v", st.Result.Items)
		}
	}
}

func call(t *testing.T, method, url string, body, out any) {
	t.Helper()

	var r io.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}

		r = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, url, r)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	b, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %s: %s", method, url, res.Status, b)
	}

	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
	}
}

// scrape is the IMS's metrics without labels, and the sums of those with.
func scrape(t *testing.T, api string) map[string]float64 {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Accept", "text/plain")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	m := map[string]float64{}

	for s := bufio.NewScanner(res.Body); s.Scan(); {
		line := s.Text()
		if line == "" || line[0] == '#' {
			continue
		}

		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}

		v, err := strconv.ParseFloat(strings.Fields(value)[0], 64)
		if err != nil {
			continue
		}

		m[name] += v
	}

	return m
}

type subscriber struct {
	imsi, imei, msisdn, tel string
}

func subscriberAt(i int) subscriber {
	return subscriber{
		imsi:   fmt.Sprintf("00101%010d", 1+i),
		imei:   fmt.Sprintf("35693%09d", i),
		msisdn: fmt.Sprintf("sip:+1555%07d@%s", i, domain),
		tel:    fmt.Sprintf("tel:+1555%07d", i),
	}
}

func (s subscriber) hss() hsstest.Subscriber {
	impi := s.imsi + "@" + domain

	return hsstest.Subscriber{
		IMPI: impi, IMSI: s.imsi, K: testK, OPc: testOPc, SQN: 32,
		IMPUs: []cx.ProfileIdentity{{Identity: "sip:" + impi, Barred: true}, {Identity: s.msisdn}, {Identity: s.tel}},
	}
}

// registerAll registers the UEs at each rate of -reg-rates in turn, and returns the subscribers registered.
func registerAll(t *testing.T, ues []*testue.UE) []int {
	t.Helper()

	var (
		mu         sync.Mutex
		registered []int
	)

	rates := parseRates(t, *regSteps)
	next := 0

	for i, rate := range rates {
		n := len(ues) - next
		if i < len(rates)-1 {
			n = min(n, int(rate*regStep.Seconds()))
		}

		if n == 0 {
			break
		}

		var l latencies

		start := time.Now()
		run(t, rate, n, func(j int) {
			k := next + j
			began := time.Now()

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			if err := ues[k].Register(ctx); err != nil {
				l.fail(err)
				return
			}

			l.add(time.Since(began))

			mu.Lock()
			registered = append(registered, k)
			mu.Unlock()
		})

		l.report(t, fmt.Sprintf("register at %4.0f/s", rate), n, time.Since(start))

		next += n
	}

	slices.Sort(registered)

	return registered
}

// callAll makes calls between the registered subscribers at each rate of -cps in turn, while they re-register every
// -rereg, and reports their setups and the IMS's CPU.
func callAll(t *testing.T, api string, registered []int) {
	t.Helper()

	// The registered subscribers are free to call, two by two: the caller and the callee of a call, until it ends.
	var (
		mu   sync.Mutex
		free = slices.Clone(registered)
	)

	rand.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })

	take := func() (int, int, bool) {
		mu.Lock()
		defer mu.Unlock()

		if len(free) < 2 {
			return 0, 0, false
		}

		a, b := free[len(free)-1], free[len(free)-2]
		free = free[:len(free)-2]

		return a, b, true
	}

	put := func(a, b int) {
		mu.Lock()
		defer mu.Unlock()

		free = append(free, a, b)
	}

	stop := make(chan struct{})

	var bg sync.WaitGroup

	// Re-registrations in the background, at the rate of the population re-registering every -rereg.
	var rereg latencies

	if *reregEach > 0 {
		rate := float64(len(registered)) / reregEach.Seconds()

		bg.Go(func() {
			tick := time.NewTicker(time.Duration(float64(time.Second) / rate))
			defer tick.Stop()

			for {
				select {
				case <-stop:
					return
				case <-tick.C:
				}

				u := uesOf(registered[rand.IntN(len(registered))])

				bg.Go(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()

					began := time.Now()
					if err := u.Reregister(ctx); err != nil {
						rereg.fail(err)
						return
					}

					rereg.add(time.Since(began))
				})
			}
		})
	}

	for _, rate := range parseRates(t, *callSteps) {
		n := int(rate * callStep.Seconds())

		var (
			l       latencies
			busy    atomic.Int64
			cpu0    = scrape(t, api)["process_cpu_seconds_total"]
			started = time.Now()
		)

		wait := start(rate, n, func(int) {
			a, b, ok := take()
			if !ok {
				busy.Add(1)
				return
			}

			defer put(a, b)

			ctx, cancel := context.WithTimeout(context.Background(), *hold+time.Minute)
			defer cancel()

			began := time.Now()

			c, err := uesOf(a).MMTel().Invite(subscriberAt(b).msisdn+";user=phone", testue.CallOptions{})
			if err != nil {
				l.fail(err)
				return
			}

			setup, setupCancel := context.WithTimeout(ctx, 30*time.Second)
			_, err = c.Wait(setup)

			setupCancel()

			if err != nil {
				l.fail(err)
				return
			}

			l.add(time.Since(began))

			select {
			case <-time.After(*hold):
			case <-c.Done():
				return
			}

			if err := c.Bye(ctx); err != nil {
				return
			}

			select {
			case <-c.Done():
			case <-ctx.Done():
			}
		})

		// The IMS at the end of the calls' arrivals, before those in progress end.
		elapsed := time.Since(started)
		m := scrape(t, api)

		wait()

		l.report(t, fmt.Sprintf("calls at %4.0f/s", rate), n-int(busy.Load()), elapsed)
		t.Logf("  IMS: %.0f%% of a CPU, %.1f MiB resident, %d calls in progress, %d calls not made: no free subscribers",
			100*(m["process_cpu_seconds_total"]-cpu0)/elapsed.Seconds(), m["process_resident_memory_bytes"]/(1<<20),
			int(m["ellaims_active_calls"]), busy.Load())
	}

	close(stop)

	rereg.report(t, "re-registrations", rereg.count(), 0)
}

var (
	uesMu  sync.Mutex
	uesAll []*testue.UE
)

func uesOf(i int) *testue.UE {
	uesMu.Lock()
	defer uesMu.Unlock()

	return uesAll[i]
}

// run calls f n times, at rate a second, each in its own goroutine, and waits for them to return.
func run(t *testing.T, rate float64, n int, f func(int)) {
	t.Helper()

	start(rate, n, f)()
}

// start calls f n times, at rate a second, each in its own goroutine, and returns once it called the last, with a
// function that waits for them to return.
func start(rate float64, n int, f func(int)) func() {
	var wg sync.WaitGroup

	begin := time.Now()

	for i := range n {
		if d := time.Until(begin.Add(time.Duration(float64(i) / rate * float64(time.Second)))); d > 0 {
			time.Sleep(d)
		}

		wg.Go(func() { f(i) })
	}

	return wg.Wait
}

func parseRates(t *testing.T, s string) []float64 {
	t.Helper()

	var out []float64

	for f := range strings.SplitSeq(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil || v <= 0 {
			t.Fatalf("bad rate %q", f)
		}

		out = append(out, v)
	}

	return out
}

// answer answers the calls of u, until it closes.
func answer(u *testue.UE) {
	for c := range u.MMTel().Calls() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), *hold+2*time.Minute)
			defer cancel()

			if err := c.Ring(ctx); err != nil {
				return
			}

			if err := c.Answer(ctx); err != nil {
				return
			}

			select {
			case <-c.Done():
			case <-ctx.Done():
			}
		}()
	}
}

// latencies are the durations of the operations that succeeded, and the errors of those that failed.
type latencies struct {
	mu     sync.Mutex
	d      []time.Duration
	errors map[string]int
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.d = append(l.d, d)
}

func (l *latencies) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.errors == nil {
		l.errors = map[string]int{}
	}

	l.errors[err.Error()]++
}

func (l *latencies) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := len(l.d)
	for _, c := range l.errors {
		n += c
	}

	return n
}

func (l *latencies) at(q float64) time.Duration {
	if len(l.d) == 0 {
		return 0
	}

	return l.d[min(len(l.d)-1, int(q*float64(len(l.d))))]
}

// report logs the operations: n attempted over elapsed, those that failed, and the latencies of the others.
func (l *latencies) report(t *testing.T, what string, n int, elapsed time.Duration) {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()

	slices.Sort(l.d)

	failed := n - len(l.d)

	rate := ""
	if elapsed > 0 {
		rate = fmt.Sprintf(" in %s (%.1f/s)", elapsed.Round(time.Second), float64(n)/elapsed.Seconds())
	}

	t.Logf("%s: %d%s, %d failed (%.3f%%), p50 %s p90 %s p99 %s max %s", what, n, rate, failed,
		100*float64(failed)/float64(max(1, n)), l.at(0.5).Round(time.Millisecond), l.at(0.9).Round(time.Millisecond),
		l.at(0.99).Round(time.Millisecond), l.at(1).Round(time.Millisecond))

	for err, c := range l.errors {
		t.Logf("  %d× %s", c, err)
	}
}
