package settings

import (
	"net/url"
)

type PolicyInterface string

const (
	PolicyNone PolicyInterface = "none"
	PolicyRx   PolicyInterface = "rx"
	PolicyN5   PolicyInterface = "n5"
)

// Policy is the policy function of the P-CSCF: the PCRF over Rx, which is the Diameter peer serving rx, or a PCF
// over N5. Without one, the P-CSCF opens no policy sessions.
type Policy struct {
	Interface PolicyInterface
	// PCFURI is the API root of the PCF over N5, http[s]://host[:port][/prefix]. The PCF must name its contexts
	// with the same scheme, host and port, so that the P-CSCF can delete those the PCF terminates after it lost
	// track of them.
	PCFURI string
}

// HTTPS reports whether N5 runs over TLS.
func (p Policy) HTTPS() bool {
	u, err := url.Parse(p.PCFURI)
	return err == nil && u.Scheme == "https"
}

func (p Policy) Validate() error {
	switch p.Interface {
	case PolicyNone, PolicyRx:
		if p.PCFURI != "" {
			return invalidf("n5 is only for the n5 interface")
		}

		return nil
	case PolicyN5:
	default:
		return invalidf("interface must be none, rx or n5")
	}

	u, err := url.Parse(p.PCFURI)

	switch {
	case p.PCFURI == "":
		return invalidf("n5.pcf_uri is required")
	case err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "":
		return invalidf("n5.pcf_uri must be http[s]://host[:port][/prefix]")
	}

	return nil
}
