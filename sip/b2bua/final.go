package b2bua

import (
	"slices"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// Best is the final response to give for the failures of several legs, as a controlling function whose invited
// members all failed: the best of their responses as a forking proxy chooses it (RFC 3261 §16.7 step 6), with the
// Warning header fields of them all (RFC 3261 §20.43). A 6xx wins; otherwise the lowest class, a response that tells
// the caller how to retry (415, 420, 484) first. The B2BUA follows no redirection and answers no challenge for its
// caller: a 3xx counts as a 480, a 401 or 407 as a 500; a 503 is turned into a 500. Without responses, as when every
// leg timed out, Best is a 408.
func Best(responses []*sip.Response) ua.Reply {
	var (
		best     *sip.Response
		code     int
		warnings []string
	)

	for _, res := range responses {
		if res == nil || res.StatusCode < 300 {
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
		return ua.Reply{Code: 408}
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

// relayable is the code a B2BUA relays for a failure.
func relayable(code int) int {
	switch {
	case code < 400:
		return 480
	case code == 401, code == 407, code == 503:
		return 500
	}

	return code
}

// better reports whether code is a better final response than best (RFC 3261 §16.7 step 6).
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
	case 401, 407, 415, 420, 484:
		return true
	}

	return false
}
