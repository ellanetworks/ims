package api

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/settings"
)

type Diameter interface {
	Identity() diameter.Identity
	Peers() []diameter.PeerStatus
}

// DiameterStatus is the identity of the IMS, derived from the operator settings, which its peers must know it by.
type DiameterStatus struct {
	Host  string `json:"host"`
	Realm string `json:"realm"`
}

func GetDiameterStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		op := cfg.Settings.Get().Operator
		writeResponse(w, DiameterStatus{Host: op.DiameterHost(), Realm: op.DiameterRealm()}, http.StatusOK, cfg.Logger)
	})
}

// DiameterPeerParams are what an operator sets of a peer. Port defaults to 3868, and transport to tcp.
type DiameterPeerParams struct {
	Host         string   `json:"host"`
	Realm        string   `json:"realm"`
	Address      string   `json:"address"`
	Port         int      `json:"port,omitempty"`
	Transport    string   `json:"transport,omitempty"`
	Applications []string `json:"applications"`
}

type DiameterPeer struct {
	ID string `json:"id"`
	DiameterPeerParams
	Status DiameterPeerStatus `json:"status"`
}

// DiameterPeerStatus is the connection to a peer. State is down until the IMS has tried it.
type DiameterPeerStatus struct {
	State         string `json:"state"`
	Since         string `json:"since,omitempty"`
	RemoteAddress string `json:"remote_address,omitempty"`
}

type DiameterPeers struct {
	Items []DiameterPeer `json:"items"`
}

func ListDiameterPeers(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		statuses := peerStatuses(cfg.Diameter)

		resp := DiameterPeers{Items: []DiameterPeer{}}
		for _, p := range cfg.Settings.Get().Peers {
			resp.Items = append(resp.Items, peerResponse(p, statuses))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetDiameterPeer(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := findPeer(cfg.Settings.Get(), r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "Diameter peer not found", nil, cfg.Logger)
			return
		}

		writeResponse(w, peerResponse(p, peerStatuses(cfg.Diameter)), http.StatusOK, cfg.Logger)
	})
}

// CreateDiameterPeer adds a peer, which the IMS then connects to.
func CreateDiameterPeer(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params DiameterPeerParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		p, err := peerSettings(params)
		if err == nil {
			p, err = cfg.Settings.CreatePeer(r.Context(), p)
		}

		if err != nil {
			writeSettingsError(w, err, "Failed to create Diameter peer", cfg.Logger)
			return
		}

		writeResponse(w, peerResponse(p, peerStatuses(cfg.Diameter)), http.StatusCreated, cfg.Logger)
	})
}

// UpdateDiameterPeer replaces a peer, which the IMS then reconnects to.
func UpdateDiameterPeer(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params DiameterPeerParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		p, err := peerSettings(params)
		if err == nil {
			p.ID = r.PathValue("id")
			err = cfg.Settings.UpdatePeer(r.Context(), p)
		}

		if err != nil {
			writeSettingsError(w, err, "Failed to update Diameter peer", cfg.Logger)
			return
		}

		writeResponse(w, peerResponse(p, peerStatuses(cfg.Diameter)), http.StatusOK, cfg.Logger)
	})
}

func DeleteDiameterPeer(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.Settings.DeletePeer(r.Context(), r.PathValue("id")); err != nil {
			writeSettingsError(w, err, "Failed to delete Diameter peer", cfg.Logger)
			return
		}

		writeResponse(w, Message{Message: "Diameter peer deleted"}, http.StatusOK, cfg.Logger)
	})
}

func peerSettings(params DiameterPeerParams) (settings.Peer, error) {
	p := settings.Peer{
		Host:      params.Host,
		Realm:     params.Realm,
		Port:      params.Port,
		Transport: settings.Transport(params.Transport),
	}

	if params.Address != "" {
		a, err := netip.ParseAddr(params.Address)
		if err != nil {
			return settings.Peer{}, settings.Invalidf("address must be an IPv4 or IPv6 address")
		}

		p.Address = a.Unmap()
	}

	if p.Port == 0 {
		p.Port = settings.DefaultDiameterPort
	}

	if p.Transport == "" {
		p.Transport = settings.TransportTCP
	}

	for _, a := range params.Applications {
		p.Applications = append(p.Applications, settings.Application(a))
	}

	return p, nil
}

func findPeer(s settings.Settings, id string) (settings.Peer, bool) {
	for _, p := range s.Peers {
		if p.ID == id {
			return p, true
		}
	}

	return settings.Peer{}, false
}

func peerStatuses(d Diameter) map[string]diameter.PeerStatus {
	statuses := make(map[string]diameter.PeerStatus)

	if d == nil {
		return statuses
	}

	for _, st := range d.Peers() {
		statuses[st.ID] = st
	}

	return statuses
}

func peerResponse(p settings.Peer, statuses map[string]diameter.PeerStatus) DiameterPeer {
	resp := DiameterPeer{
		ID: p.ID,
		DiameterPeerParams: DiameterPeerParams{
			Host:         p.Host,
			Realm:        p.Realm,
			Address:      p.Address.String(),
			Port:         p.Port,
			Transport:    string(p.Transport),
			Applications: []string{},
		},
		Status: DiameterPeerStatus{State: "down"},
	}

	for _, a := range p.Applications {
		resp.Applications = append(resp.Applications, string(a))
	}

	if st, ok := statuses[p.ID]; ok {
		resp.Status.State = st.State.String()

		if !st.Since.IsZero() {
			resp.Status.Since = formatTime(st.Since)
		}

		if st.RemoteAddr.IsValid() {
			resp.Status.RemoteAddress = st.RemoteAddr.Unmap().String()
		}
	}

	return resp
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
