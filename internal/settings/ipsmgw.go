package settings

type IPSMGW struct {
	Number string
}

func (g IPSMGW) Validate() error {
	if g.Number != "" && (!isDigits(g.Number) || len(g.Number) > maxE164Digits) {
		return invalidf("ip_sm_gw.number must be an E.164 number of up to %d digits", maxE164Digits)
	}

	return nil
}
