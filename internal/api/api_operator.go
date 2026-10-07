package api

import (
	"net/http"

	"github.com/ellanetworks/ims/internal/settings"
)

type Operator struct {
	MCC       string    `json:"mcc"`
	MNC       string    `json:"mnc"`
	Numbering Numbering `json:"numbering"`
}

type Numbering struct {
	CountryCode         string `json:"country_code"`
	NationalPrefix      string `json:"national_prefix"`
	InternationalPrefix string `json:"international_prefix"`
}

func GetOperator(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, operatorResponse(cfg.Settings.Get().Operator), http.StatusOK, cfg.Logger)
	})
}

// UpdateOperator replaces the operator settings. A change of MCC or MNC renames the IMS, which restarts Diameter
// and SIP under the new names.
func UpdateOperator(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Operator
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		o := settings.Operator{
			MCC: p.MCC,
			MNC: p.MNC,
			Numbering: settings.Numbering{
				CountryCode:         p.Numbering.CountryCode,
				NationalPrefix:      p.Numbering.NationalPrefix,
				InternationalPrefix: p.Numbering.InternationalPrefix,
			},
		}

		if err := cfg.Settings.UpdateOperator(r.Context(), o); err != nil {
			writeSettingsError(w, err, "Failed to update operator", cfg.Logger)
			return
		}

		writeResponse(w, operatorResponse(cfg.Settings.Get().Operator), http.StatusOK, cfg.Logger)
	})
}

func operatorResponse(o settings.Operator) Operator {
	return Operator{
		MCC: o.MCC,
		MNC: o.MNC,
		Numbering: Numbering{
			CountryCode:         o.Numbering.CountryCode,
			NationalPrefix:      o.Numbering.NationalPrefix,
			InternationalPrefix: o.Numbering.InternationalPrefix,
		},
	}
}
