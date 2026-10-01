package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
)

type Diameter interface {
	Identity() diameter.Identity
	Peers() []diameter.PeerStatus
}

type DiameterPeer struct {
	ID           string   `json:"id"`
	Host         string   `json:"host"`
	Realm        string   `json:"realm"`
	Transport    string   `json:"transport"`
	Address      string   `json:"address"`
	State        string   `json:"state"`
	Since        string   `json:"since"`
	Applications []string `json:"applications"`
}

type DiameterStatus struct {
	Host  string         `json:"host"`
	Realm string         `json:"realm"`
	Peers []DiameterPeer `json:"peers"`
}

func GetDiameterStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		identity := cfg.Diameter.Identity()

		resp := DiameterStatus{
			Host:  identity.OriginHost,
			Realm: identity.OriginRealm,
			Peers: []DiameterPeer{},
		}

		for _, p := range cfg.Diameter.Peers() {
			peer := DiameterPeer{
				ID:           p.ID,
				Host:         p.Host,
				Realm:        p.Realm,
				Transport:    p.Transport.String(),
				State:        p.State.String(),
				Since:        formatTime(p.Since),
				Applications: []string{},
			}

			if p.RemoteAddr.IsValid() {
				peer.Address = p.RemoteAddr.Unmap().String()
			}

			for _, a := range p.Applications {
				peer.Applications = append(peer.Applications, applicationName(a))
			}

			resp.Peers = append(resp.Peers, peer)
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func applicationName(a diameter.Application) string {
	switch a.ID {
	case cx.ApplicationID:
		return "cx"
	case rx.ApplicationID:
		return "rx"
	default:
		return strconv.FormatUint(uint64(a.ID), 10)
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
