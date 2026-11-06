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
	MinNationalDigits   int    `json:"min_national_digits"`
}

func GetOperator(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, operatorResponse(cfg.Settings.Get().Operator), http.StatusOK, cfg.Logger)
	})
}

func UpdateOperator(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := Operator{Numbering: Numbering{MinNationalDigits: settings.DefaultMinNationalDigits}}
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
				MinNationalDigits:   p.Numbering.MinNationalDigits,
			},
		}

		if err := cfg.Settings.UpdateOperator(r.Context(), o); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update operator", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, operatorResponse(cfg.Settings.Get().Operator), http.StatusOK, cfg.Logger)
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
			MinNationalDigits:   o.Numbering.MinNationalDigits,
		},
	}
}
