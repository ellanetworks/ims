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

// DiameterPeerParams are what an operator sets of a peer. Host is optional: empty takes the one the peer gives in
// its capabilities exchange. Port defaults to 3868, transport to tcp, and priority to 10.
type DiameterPeerParams struct {
	Host         string   `json:"host"`
	Address      string   `json:"address"`
	Port         int      `json:"port,omitempty"`
	Transport    string   `json:"transport,omitempty"`
	Applications []string `json:"applications"`
	Priority     *int     `json:"priority,omitempty"`
}

type DiameterPeer struct {
	ID string `json:"id"`
	DiameterPeerParams
	Status DiameterPeerStatus `json:"status"`
}

// DiameterPeerStatus is the connection to a peer. State is down until the IMS has tried it. Host and realm are
// the ones the peer gave in its last capabilities exchange, the host else the configured one. Error is why the
// last connection failed.
type DiameterPeerStatus struct {
	State         string `json:"state"`
	Since         string `json:"since,omitempty"`
	RemoteAddress string `json:"remote_address,omitempty"`
	Host          string `json:"host,omitempty"`
	Realm         string `json:"realm,omitempty"`
	Error         string `json:"error,omitempty"`
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
		Port:      params.Port,
		Transport: settings.Transport(params.Transport),
		Priority:  settings.DefaultPriority,
	}

	if params.Priority != nil {
		p.Priority = *params.Priority
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
			Address:      p.Address.String(),
			Port:         p.Port,
			Transport:    string(p.Transport),
			Applications: []string{},
			Priority:     &p.Priority,
		},
		Status: peerStatus(p, statuses),
	}

	for _, a := range p.Applications {
		resp.Applications = append(resp.Applications, string(a))
	}

	return resp
}

func peerStatus(p settings.Peer, statuses map[string]diameter.PeerStatus) DiameterPeerStatus {
	status := DiameterPeerStatus{State: "down"}

	st, ok := statuses[p.ID]
	if !ok {
		return status
	}

	status.State = st.State.String()

	if !st.Since.IsZero() {
		status.Since = formatTime(st.Since)
	}

	if st.RemoteAddr.IsValid() {
		status.RemoteAddress = st.RemoteAddr.Unmap().String()
	}

	status.Host, status.Realm, status.Error = st.Host, st.Realm, st.LastError

	return status
}

// DiameterRouteParams are what an operator sets of a route: the realm of the HSS or the PCRF. Empty is the home
// domain.
type DiameterRouteParams struct {
	Realm string `json:"realm"`
}

// DiameterRoute is where the requests of an application go: to the destination realm, through its peers in the
// order they are tried.
type DiameterRoute struct {
	Application      string `json:"application"`
	Realm            string `json:"realm"`
	DestinationRealm string `json:"destination_realm"`
	DiameterRoutePeers
}

type DiameterRoutePeers struct {
	Peers []DiameterRoutePeer `json:"peers"`
}

type DiameterRoutePeer struct {
	ID       string             `json:"id"`
	Host     string             `json:"host"`
	Priority int                `json:"priority"`
	Status   DiameterPeerStatus `json:"status"`
}

type DiameterRoutes struct {
	Items []DiameterRoute `json:"items"`
}

func ListDiameterRoutes(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s, statuses := cfg.Settings.Get(), peerStatuses(cfg.Diameter)

		resp := DiameterRoutes{Items: []DiameterRoute{}}
		for _, r := range s.Routes {
			resp.Items = append(resp.Items, routeResponse(s, r, statuses))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetDiameterRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := cfg.Settings.Get()

		route, ok := findRoute(s, r.PathValue("application"))
		if !ok {
			writeError(w, http.StatusNotFound, "Diameter route not found", nil, cfg.Logger)
			return
		}

		writeResponse(w, routeResponse(s, route, peerStatuses(cfg.Diameter)), http.StatusOK, cfg.Logger)
	})
}

// UpdateDiameterRoute sets the realm of an application's requests, which the IMS applies to the next request.
func UpdateDiameterRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params DiameterRouteParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		route := settings.Route{Application: settings.Application(r.PathValue("application")), Realm: params.Realm}

		if err := cfg.Settings.UpdateRoute(r.Context(), route); err != nil {
			writeSettingsError(w, err, "Failed to update Diameter route", cfg.Logger)
			return
		}

		writeResponse(w, routeResponse(cfg.Settings.Get(), route, peerStatuses(cfg.Diameter)), http.StatusOK, cfg.Logger)
	})
}

func findRoute(s settings.Settings, app string) (settings.Route, bool) {
	for _, r := range s.Routes {
		if string(r.Application) == app {
			return r, true
		}
	}

	return settings.Route{}, false
}

func routeResponse(s settings.Settings, r settings.Route, statuses map[string]diameter.PeerStatus) DiameterRoute {
	resp := DiameterRoute{
		Application:        string(r.Application),
		Realm:              r.Realm,
		DestinationRealm:   s.Realm(r.Application),
		DiameterRoutePeers: DiameterRoutePeers{Peers: []DiameterRoutePeer{}},
	}

	for _, p := range s.Serving(r.Application) {
		resp.Peers = append(resp.Peers, DiameterRoutePeer{
			ID: p.ID, Host: p.Host, Priority: p.Priority, Status: peerStatus(p, statuses),
		})
	}

	return resp
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
