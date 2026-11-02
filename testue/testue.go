package testue

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/internal/usim"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/ue"
)

type Config struct {
	IMSI       string
	MNCLength  int
	IMPI       string
	IMPU       string
	HomeDomain string

	K   []byte
	OPc []byte

	SQN uint64

	IMEI string

	PCSCF     netip.AddrPort
	Local     netip.Addr
	Transport sip.Transport

	Offers []ue.Offer

	ContactParams sip.Params

	RegID int64

	Plain bool

	AccessNetworkInfo string

	Expires time.Duration

	NoRegEvent bool

	MMTel   MMTelConfig
	NoMMTel bool

	NoSMS bool

	Services []ue.Service

	LargeUDP bool

	TCPResponsesFromClientPort bool

	T1 time.Duration

	Kernel ue.IPsec

	Control func(network, address string, c syscall.RawConn) error

	Clock transaction.Clock

	Do func(f func())

	Trace func(m sip.Message, f sip.Flow, sent bool)

	Logger *slog.Logger

	EventBuffer int
}

type UE struct {
	*ue.UE

	base atomic.Pointer[ue.UE]

	cfg    Config
	usim   *usim.USIM
	events chan ue.Event
	mmtel  *MMTel
	sms    *SMS

	mu      sync.Mutex
	dropped int
}

func New(cfg Config) (*UE, error) {
	var (
		id  ue.Identities
		err error
	)

	if cfg.IMSI != "" {
		mncLength := cfg.MNCLength
		if mncLength == 0 {
			mncLength = 2
		}

		if id, err = ue.IdentitiesFromIMSI(cfg.IMSI, mncLength); err != nil {
			return nil, fmt.Errorf("testue: %w", err)
		}

		if cfg.AccessNetworkInfo == "" && !cfg.Plain {
			cfg.AccessNetworkInfo = accessNetworkInfo(cfg.IMSI[:3], cfg.IMSI[3:3+mncLength])
		}
	}

	if cfg.IMPI != "" {
		id.IMPI = cfg.IMPI
	}

	if cfg.IMPU != "" {
		id.IMPU = cfg.IMPU
	}

	if cfg.HomeDomain != "" {
		id.HomeDomain = cfg.HomeDomain
	}

	if id.IMPI == "" || id.IMPU == "" || id.HomeDomain == "" {
		return nil, errors.New("testue: no IMSI, and no IMPI, IMPU and home domain")
	}

	if cfg.AccessNetworkInfo == "" && !cfg.Plain {
		return nil, errors.New("testue: no AccessNetworkInfo, and no IMSI to derive it from")
	}

	instance, err := ue.IMEIInstanceID(cfg.IMEI)
	if err != nil {
		return nil, fmt.Errorf("testue: %w", err)
	}

	t := &UE{
		cfg:    cfg,
		usim:   usim.New(cfg.K, cfg.OPc, cfg.SQN),
		events: make(chan ue.Event, cmp.Or(cfg.EventBuffer, 1024)),
	}

	var services []ue.Service

	if !cfg.NoMMTel {
		t.mmtel = newMMTel(t, cfg.MMTel)
		services = append(services, t.mmtel.service())
	}

	if !cfg.NoSMS {
		t.sms = &SMS{u: t}
		services = append(services, t.sms.service())
	}

	core, err := ue.New(ue.Config{
		Identities:                 id,
		InstanceID:                 instance,
		AKA:                        authenticator{t.usim},
		PCSCF:                      cfg.PCSCF,
		Local:                      cfg.Local,
		Transport:                  cfg.Transport,
		Offers:                     cfg.Offers,
		ContactParams:              cfg.ContactParams,
		RegID:                      cfg.RegID,
		Plain:                      cfg.Plain,
		AccessNetworkInfo:          cfg.AccessNetworkInfo,
		Expires:                    cfg.Expires,
		NoRegEvent:                 cfg.NoRegEvent,
		Services:                   append(services, cfg.Services...),
		LargeUDP:                   cfg.LargeUDP,
		TCPResponsesFromClientPort: cfg.TCPResponsesFromClientPort,
		T1:                         cfg.T1,
		IPsec:                      cfg.Kernel,
		Control:                    cfg.Control,
		Clock:                      cfg.Clock,
		Do:                         cfg.Do,
		Trace:                      cfg.Trace,
		OnEvent:                    t.event,
		Logger:                     cfg.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("testue: %w", err)
	}

	t.UE = core
	t.base.Store(core)

	return t, nil
}

func (u *UE) core() *ue.UE { return u.base.Load() }

func accessNetworkInfo(mcc, mnc string) string {
	return "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=" + mcc + mnc + "0001" + "0000001"
}

type authenticator struct{ u *usim.USIM }

func (a authenticator) Authenticate(rand, autn []byte) (ue.AKAResult, error) {
	res, ck, ik, auts, err := a.u.Authenticate(rand, autn)
	if errors.Is(err, usim.ErrMACFailure) {
		return ue.AKAResult{}, ue.ErrMACFailure
	}

	return ue.AKAResult{RES: res, CK: ck, IK: ik, AUTS: auts}, err
}

func (u *UE) MMTel() *MMTel { return u.mmtel }

func (u *UE) SMS() *SMS { return u.sms }

func (u *UE) SQN() uint64 { return u.usim.SQN() }

func (u *UE) Events() <-chan ue.Event { return u.events }

func (u *UE) Dropped() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.dropped
}

func (u *UE) event(e ue.Event) {
	select {
	case u.events <- e:
	default:
		u.mu.Lock()
		u.dropped++
		u.mu.Unlock()
	}
}
