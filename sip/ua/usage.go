package ua

import (
	"errors"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// RFC 5057 section 5.1
// RFC 5057 section 5.2
type effect int

const (
	affectsTransaction effect = iota
	destroysUsage
	destroysDialog
)

// RFC 5057 section 5.3
func usageOf(method string) string {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "ACK", "CANCEL", "BYE", "INFO":
		return "invite"
	case "SUBSCRIBE", "NOTIFY", "REFER":
		return "subscribe"
	}

	return ""
}

// RFC 5057 section 5.1
func integral(method string) bool {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "BYE", "SUBSCRIBE", "NOTIFY", "REFER":
		return true
	}

	return false
}

// RFC 5057 section 5.1 table 2
// RFC 5057 section 5.2
func failureOf(method string, res *sip.Response, err error) effect {
	usage := usageOf(method) != ""

	if res == nil {
		if usage && errors.Is(err, transaction.ErrTimeout) {
			return destroysUsage
		}

		return affectsTransaction
	}

	switch code := res.StatusCode; code {
	case 404, 410, 416, 482, 483, 484, 485, 502, 604:
		return destroysDialog
	case 481:
		if method == "CANCEL" || !usage {
			return affectsTransaction
		}

		return destroysUsage
	case 480, 408:
		if !usage {
			return affectsTransaction
		}

		return destroysUsage
	case 405, 501:
		if integral(method) {
			return destroysUsage
		}
	case 489:
		if usageOf(method) == "subscribe" {
			return destroysUsage
		}
	}

	return affectsTransaction
}
