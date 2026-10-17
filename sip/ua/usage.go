package ua

import (
	"errors"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// effect is what a failure of a request within a dialog does to the dialog and its usages (RFC 5057 §5.1, §5.2).
type effect int

const (
	// affectsTransaction: the failure is a complaint about the transaction only.
	affectsTransaction effect = iota
	// destroysUsage: the failure ends the usage the request belongs to, not the other usages of the dialog.
	destroysUsage
	// destroysDialog: the failure ends the dialog and all its usages.
	destroysDialog
)

// usageOf is the usage a method within a dialog belongs to (RFC 5057 §5.3): "invite" for INVITE, UPDATE, PRACK, ACK,
// CANCEL, BYE and INFO; "subscribe" for SUBSCRIBE, NOTIFY and REFER; none for OPTIONS, MESSAGE and the unknown methods.
func usageOf(method string) string {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "ACK", "CANCEL", "BYE", "INFO":
		return "invite"
	case "SUBSCRIBE", "NOTIFY", "REFER":
		return "subscribe"
	}

	return ""
}

// integral reports a method the usage cannot do without: a 405 or a 501 to it ends the usage, while one to INFO in an
// invite usage, say, ends the transaction only (RFC 5057 §5.1 note 3).
func integral(method string) bool {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "BYE", "SUBSCRIBE", "NOTIFY", "REFER":
		return true
	}

	return false
}

// failureOf classifies the outcome of a request within a dialog, res its final response other than 2xx, or err the
// error of its transaction (RFC 5057 Table 2, §5.2). A request that belongs to no usage only ever affects the dialog.
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
