package settings

// IPSMGW are the settings of the built-in IP-SM-GW.
type IPSMGW struct {
	// Number is the E.164 number of the IP-SM-GW, which it registers at the HSS for the users it serves (TS 29.328
	// §6.1.2.1 step 4b) and gives the SMS-GMSC as its address (TS 24.341 §5.3.3.3). Empty until set: the IP-SM-GW
	// registers no user at the HSS.
	Number string
}

func (g IPSMGW) Validate() error {
	if g.Number != "" && (!isDigits(g.Number) || len(g.Number) > maxE164Digits) {
		return invalidf("ip_sm_gw.number must be an E.164 number of up to %d digits", maxE164Digits)
	}

	return nil
}
