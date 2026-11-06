package api

import (
	"crypto/x509"
	"net/http"
	"net/netip"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip/transport"
)

type SIPTrunkAddress struct {
	Host      string `json:"host"`
	Port      *int   `json:"port,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type SIPTrunkParams struct {
	Name              string             `json:"name"`
	Addresses         []SIPTrunkAddress  `json:"addresses"`
	InboundCIDRs      []string           `json:"inbound_cidrs"`
	Domain            string             `json:"domain"`
	Trusted           bool               `json:"trusted"`
	KeepaliveInterval *int               `json:"keepalive_interval,omitempty"`
	Enabled           *bool              `json:"enabled"`
	TLS               *SIPTrunkTLSParams `json:"tls,omitempty"`
}

type SIPTrunkTLSParams struct {
	Required                 bool     `json:"required"`
	RequireClientCertificate bool     `json:"require_client_certificate"`
	CACertificates           string   `json:"ca_certificates"`
	PinnedCertificates       string   `json:"pinned_certificates"`
	PinnedFingerprints       []string `json:"pinned_fingerprints"`
	Identities               []string `json:"identities"`
}

type SIPTrunkTLS struct {
	SIPTrunkTLSParams
	CACertificateDetails     []CertificateSummary `json:"ca_certificate_details"`
	PinnedCertificateDetails []CertificateSummary `json:"pinned_certificate_details"`
}

type CertificateSummary struct {
	Subject           string   `json:"subject"`
	Issuer            string   `json:"issuer"`
	DNSNames          []string `json:"dns_names"`
	URIs              []string `json:"uris"`
	IPAddresses       []string `json:"ip_addresses"`
	Identities        []string `json:"identities"`
	CA                bool     `json:"ca"`
	NotBefore         string   `json:"not_before"`
	NotAfter          string   `json:"not_after"`
	SHA256Fingerprint string   `json:"sha256_fingerprint"`
}

type SIPTrunk struct {
	ID string `json:"id"`
	SIPTrunkParams
	TLS    SIPTrunkTLS    `json:"tls"`
	Status SIPTrunkStatus `json:"status"`
}

type SIPTrunkStatus struct {
	State settings.TrunkState `json:"state"`
	Since string              `json:"since,omitempty"`
}

type SIPTrunkList struct {
	Items []SIPTrunk `json:"items"`
}

func ListSIPTrunks(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SIPTrunkList{Items: []SIPTrunk{}}
		for _, t := range cfg.Settings.Get().Trunks {
			resp.Items = append(resp.Items, trunkResponse(t, cfg.SIPTrunks))
		}

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetSIPTrunk(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := cfg.Settings.Get().Trunk(r.PathValue("id"))
		if !ok {
			writeError(r.Context(), w, http.StatusNotFound, "SIP trunk not found", nil, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, trunkResponse(t, cfg.SIPTrunks), http.StatusOK, cfg.Logger)
	})
}

func CreateSIPTrunk(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params SIPTrunkParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		t, err := trunkSettings(params)
		if err == nil {
			t, err = cfg.Settings.CreateTrunk(r.Context(), t)
		}

		if err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to create SIP trunk", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, trunkResponse(t, cfg.SIPTrunks), http.StatusCreated, cfg.Logger)
	})
}

func UpdateSIPTrunk(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params SIPTrunkParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		t, err := trunkSettings(params)
		if err == nil {
			t.ID = r.PathValue("id")
			err = cfg.Settings.UpdateTrunk(r.Context(), t)
		}

		if err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update SIP trunk", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, trunkResponse(t, cfg.SIPTrunks), http.StatusOK, cfg.Logger)
	})
}

func DeleteSIPTrunk(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.Settings.DeleteTrunk(r.Context(), r.PathValue("id")); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to delete SIP trunk", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, Message{Message: "SIP trunk deleted"}, http.StatusOK, cfg.Logger)
	})
}

func trunkSettings(params SIPTrunkParams) (settings.Trunk, error) {
	t := settings.Trunk{
		Name:              params.Name,
		Domain:            params.Domain,
		Trusted:           params.Trusted,
		KeepaliveInterval: settings.DefaultKeepaliveInterval,
	}

	if params.KeepaliveInterval != nil {
		d, err := settings.KeepaliveSeconds(*params.KeepaliveInterval)
		if err != nil {
			return settings.Trunk{}, err
		}

		t.KeepaliveInterval = d
	}

	if params.Enabled == nil {
		return settings.Trunk{}, settings.Invalidf("enabled is required")
	}

	t.Enabled = *params.Enabled

	if ip, err := netip.ParseAddr(params.Domain); err == nil && ip.Zone() == "" {
		t.Domain = ip.Unmap().String()
	}

	for _, a := range params.Addresses {
		addr := settings.TrunkAddress{Host: a.Host, Transport: settings.SIPTransport(a.Transport)}

		if ip, err := netip.ParseAddr(a.Host); err == nil && ip.Zone() == "" {
			addr.Host = ip.Unmap().String()
		}

		if a.Port != nil {
			if *a.Port < 1 || *a.Port > 65535 {
				return settings.Trunk{}, settings.Invalidf("addresses must have a port between 1 and 65535, or none")
			}

			addr.Port = *a.Port
		}

		t.Addresses = append(t.Addresses, addr)
	}

	for _, c := range params.InboundCIDRs {
		p, err := parseInboundCIDR(c)
		if err != nil {
			return settings.Trunk{}, err
		}

		t.InboundCIDRs = append(t.InboundCIDRs, p)
	}

	if params.TLS != nil {
		tls, err := trunkTLSSettings(*params.TLS)
		if err != nil {
			return settings.Trunk{}, err
		}

		t.TLS = tls
	}

	return t, nil
}

func trunkTLSSettings(params SIPTrunkTLSParams) (settings.TrunkTLS, error) {
	tls := settings.TrunkTLS{
		Required:                 params.Required,
		RequireClientCertificate: params.RequireClientCertificate,
		Identities:               append([]string(nil), params.Identities...),
	}

	var err error

	if tls.CACertificates, err = settings.ParseCertificatesPEM("tls.ca_certificates", params.CACertificates); err != nil {
		return settings.TrunkTLS{}, err
	}

	if tls.PinnedCertificates, err = settings.ParseCertificatesPEM("tls.pinned_certificates", params.PinnedCertificates); err != nil {
		return settings.TrunkTLS{}, err
	}

	if len(params.PinnedFingerprints) > settings.MaxTrunkFingerprints {
		return settings.TrunkTLS{}, settings.Invalidf("tls.pinned_fingerprints must list at most %d fingerprints",
			settings.MaxTrunkFingerprints)
	}

	for _, s := range params.PinnedFingerprints {
		fp, err := settings.ParseFingerprint(s)
		if err != nil {
			return settings.TrunkTLS{}, err
		}

		tls.PinnedFingerprints = append(tls.PinnedFingerprints, fp)
	}

	return tls, nil
}

func parseInboundCIDR(s string) (netip.Prefix, error) {
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}

	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, settings.Invalidf("inbound_cidrs must be IPv4 or IPv6 networks, not %q", s)
	}

	return p, nil
}

func trunkResponse(t settings.Trunk, states settings.TrunkStates) SIPTrunk {
	keepalive := int(t.KeepaliveInterval / time.Second)

	resp := SIPTrunk{
		ID: t.ID,
		SIPTrunkParams: SIPTrunkParams{
			Name:              t.Name,
			Addresses:         []SIPTrunkAddress{},
			InboundCIDRs:      []string{},
			Domain:            t.Domain,
			Trusted:           t.Trusted,
			KeepaliveInterval: &keepalive,
			Enabled:           &t.Enabled,
		},
		Status: trunkStatus(t, states),
	}

	for _, a := range t.Addresses {
		addr := SIPTrunkAddress{Host: a.Host, Transport: string(a.Transport)}
		if a.Port != 0 {
			addr.Port = &a.Port
		}

		resp.Addresses = append(resp.Addresses, addr)
	}

	for _, p := range t.InboundCIDRs {
		resp.InboundCIDRs = append(resp.InboundCIDRs, p.String())
	}

	resp.TLS = trunkTLSResponse(t.TLS)

	return resp
}

func trunkTLSResponse(t settings.TrunkTLS) SIPTrunkTLS {
	resp := SIPTrunkTLS{
		SIPTrunkTLSParams: SIPTrunkTLSParams{
			Required:                 t.Required,
			RequireClientCertificate: t.RequireClientCertificate,
			CACertificates:           settings.EncodeCertificatesPEM(t.CACertificates),
			PinnedCertificates:       settings.EncodeCertificatesPEM(t.PinnedCertificates),
			PinnedFingerprints:       []string{},
			Identities:               []string{},
		},
		CACertificateDetails:     certificateSummaries(t.CACertificates),
		PinnedCertificateDetails: certificateSummaries(t.PinnedCertificates),
	}

	for _, fp := range t.PinnedFingerprints {
		resp.PinnedFingerprints = append(resp.PinnedFingerprints, settings.FormatFingerprint(fp))
	}

	resp.Identities = append(resp.Identities, t.Identities...)

	return resp
}

func certificateSummaries(ders [][]byte) []CertificateSummary {
	out := []CertificateSummary{}

	for _, der := range ders {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}

		s := CertificateSummary{
			Subject:           c.Subject.String(),
			Issuer:            c.Issuer.String(),
			DNSNames:          append([]string{}, c.DNSNames...),
			URIs:              []string{},
			IPAddresses:       []string{},
			Identities:        append([]string{}, transport.CertificateIdentities(c)...),
			CA:                c.BasicConstraintsValid && c.IsCA,
			NotBefore:         formatTime(c.NotBefore),
			NotAfter:          formatTime(c.NotAfter),
			SHA256Fingerprint: settings.FormatFingerprint(transport.Fingerprint(c)),
		}

		for _, u := range c.URIs {
			s.URIs = append(s.URIs, u.String())
		}

		for _, ip := range c.IPAddresses {
			s.IPAddresses = append(s.IPAddresses, ip.String())
		}

		out = append(out, s)
	}

	return out
}

func trunkStatus(t settings.Trunk, states settings.TrunkStates) SIPTrunkStatus {
	st := settings.StatusOf(states, t.ID)
	status := SIPTrunkStatus{State: st.State}

	if !st.Since.IsZero() {
		status.Since = formatTime(st.Since)
	}

	return status
}

type SIPRouteParams struct {
	Prefix string   `json:"prefix"`
	Trunks []string `json:"trunks"`
}

type SIPRoute struct {
	ID string `json:"id"`
	SIPRouteParams
}

type SIPRouteList struct {
	Items []SIPRoute `json:"items"`
}

func ListSIPRoutes(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SIPRouteList{Items: []SIPRoute{}}
		for _, route := range cfg.Settings.Get().SIPRoutes {
			resp.Items = append(resp.Items, sipRouteResponse(route))
		}

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetSIPRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := cfg.Settings.Get().SIPRoute(r.PathValue("id"))
		if !ok {
			writeError(r.Context(), w, http.StatusNotFound, "SIP route not found", nil, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, sipRouteResponse(route), http.StatusOK, cfg.Logger)
	})
}

func CreateSIPRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params SIPRouteParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		route, err := cfg.Settings.CreateSIPRoute(r.Context(), settings.SIPRoute{Prefix: params.Prefix, Trunks: params.Trunks})
		if err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to create SIP route", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, sipRouteResponse(route), http.StatusCreated, cfg.Logger)
	})
}

func UpdateSIPRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params SIPRouteParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		route := settings.SIPRoute{ID: r.PathValue("id"), Prefix: params.Prefix, Trunks: params.Trunks}

		if err := cfg.Settings.UpdateSIPRoute(r.Context(), route); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update SIP route", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, sipRouteResponse(route), http.StatusOK, cfg.Logger)
	})
}

func DeleteSIPRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.Settings.DeleteSIPRoute(r.Context(), r.PathValue("id")); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to delete SIP route", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, Message{Message: "SIP route deleted"}, http.StatusOK, cfg.Logger)
	})
}

func sipRouteResponse(r settings.SIPRoute) SIPRoute {
	resp := SIPRoute{ID: r.ID, SIPRouteParams: SIPRouteParams{Prefix: r.Prefix, Trunks: []string{}}}
	resp.Trunks = append(resp.Trunks, r.Trunks...)

	return resp
}
