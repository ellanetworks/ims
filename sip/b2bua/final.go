package b2bua

import (
	"slices"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

type Failure struct {
	Response *sip.Response
	Err      error
}

// RFC 3261 section 16.7
// RFC 3261 section 20.43
func Best(failures []Failure) ua.Reply {
	var (
		best     *sip.Response
		code     int
		warnings []string
		err      error
	)

	for _, f := range failures {
		res := f.Response
		if res == nil || res.StatusCode < 300 {
			if err == nil {
				err = f.Err
			}

			continue
		}

		for _, w := range res.Header.Values("Warning") {
			if !slices.Contains(warnings, w) {
				warnings = append(warnings, w)
			}
		}

		c := relayable(res.StatusCode)
		if best == nil || better(c, code) {
			best, code = res, c
		}
	}

	if best == nil {
		if err == nil {
			return ua.Reply{Code: 408}
		}

		return FinalFor(nil, err)
	}

	rp := ua.Reply{Code: code}
	if code == best.StatusCode {
		rp.Reason = best.Reason
	}

	for _, w := range warnings {
		rp.Header.Add("Warning", w)
	}

	return rp
}

func relayable(code int) int {
	switch {
	case code < 400:
		return 480
	case code == 401, code == 407, code == 503:
		return 500
	}

	return code
}

// RFC 3261 section 16.7
func better(code, best int) bool {
	switch {
	case best >= 600:
		return false
	case code >= 600:
		return true
	case code/100 != best/100:
		return code/100 < best/100
	}

	return retryInformation(code) && !retryInformation(best)
}

func retryInformation(code int) bool {
	switch code {
	case 415, 420, 484:
		return true
	}

	return false
}
