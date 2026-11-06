package b2bua

import (
	"errors"
	"slices"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type Failure struct {
	Response *sip.Response
	Err      error
}

// RFC 3263 section 4.3
func (f Failure) ServerFailed() bool {
	if f.Response != nil {
		return f.Response.StatusCode == 503
	}

	return errors.Is(f.Err, transaction.ErrTransport) || errors.Is(f.Err, transaction.ErrTimeout)
}

// RFC 3261 section 8.1.3.1
// RFC 3261 section 16.7
// RFC 3261 section 16.9
func chosen(failures []Failure) Failure {
	best, code, timeout := -1, 0, false

	for i, f := range failures {
		var c int

		switch {
		case f.Response != nil:
			c = relayable(f.Response.StatusCode)
		case errors.Is(f.Err, transaction.ErrTransport):
			c = relayable(503)
		case errors.Is(f.Err, transaction.ErrTimeout):
			c = 408
		default:
			continue
		}

		synthetic := f.Response == nil && c == 408

		if best < 0 || better(c, code) || timeout && !synthetic && c/100 == code/100 {
			best, code, timeout = i, c, synthetic
		}
	}

	if best < 0 {
		return failures[len(failures)-1]
	}

	return failures[best]
}

// RFC 3261 section 8.1.3.1
// RFC 3261 section 16.7
// RFC 3261 section 20.43
func Best(failures []Failure) ua.Reply {
	var (
		best     *sip.Response
		code     int
		found    bool
		warnings []string
		err      error
	)

	for _, f := range failures {
		var c int

		res := f.Response

		switch {
		case res != nil && res.StatusCode >= 300:
			for _, w := range res.Header.Values("Warning") {
				if !slices.Contains(warnings, w) {
					warnings = append(warnings, w)
				}
			}

			c = relayable(res.StatusCode)
		case res == nil && errors.Is(f.Err, transaction.ErrTimeout):
			c = 408
		default:
			if err == nil {
				err = f.Err
			}

			continue
		}

		if !found || better(c, code) || best == nil && res != nil && c/100 == code/100 {
			best, code, found = res, c, true
		}
	}

	if !found {
		if err == nil {
			return ua.Reply{Code: 408}
		}

		return FinalFor(nil, err)
	}

	rp := ua.Reply{Code: code}
	if best != nil && code == best.StatusCode {
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
