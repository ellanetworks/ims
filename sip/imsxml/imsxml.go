package imsxml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

const ContentType = "application/3gpp-ims+xml"

const Version = "1"

const (
	TypeEmergency   = "emergency"
	TypeRestoration = "restoration"
)

const (
	ActionEmergencyRegistration  = "emergency-registration"
	ActionInitialRegistration    = "initial-registration"
	ActionAnonymousEmergencyCall = "anonymous-emergencycall"
)

const maxDocumentSize = 1 << 16

type IMS3GPP struct {
	Version            string
	AlternativeService *AlternativeService
	ServiceInfo        string
}

type AlternativeService struct {
	Type   string
	Reason string
	Action string
}

func Emergency(reason string) IMS3GPP {
	return IMS3GPP{Version: Version, AlternativeService: &AlternativeService{Type: TypeEmergency, Reason: reason}}
}

type xmlIMS3GPP struct {
	XMLName            xml.Name               `xml:"ims-3gpp"`
	Version            string                 `xml:"version,attr"`
	AlternativeService *xmlAlternativeService `xml:"alternative-service,omitempty"`
	ServiceInfo        *string                `xml:"service-info,omitempty"`
}

type xmlAlternativeService struct {
	Type   string `xml:"type"`
	Reason string `xml:"reason"`
	Action string `xml:"action,omitempty"`
}

func Encode(d IMS3GPP) ([]byte, error) {
	if err := validate(d); err != nil {
		return nil, fmt.Errorf("encode ims-3gpp: %w", err)
	}

	doc := xmlIMS3GPP{Version: d.Version}

	if a := d.AlternativeService; a != nil {
		doc.AlternativeService = &xmlAlternativeService{Type: a.Type, Reason: a.Reason, Action: a.Action}
	} else {
		doc.ServiceInfo = &d.ServiceInfo
	}

	out, err := xml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode ims-3gpp: %w", err)
	}

	return append([]byte(xml.Header), out...), nil
}

func validate(d IMS3GPP) error {
	if !isDecimal(d.Version) {
		return fmt.Errorf("version %q is not a decimal", d.Version)
	}

	if d.AlternativeService != nil {
		if d.ServiceInfo != "" {
			return errors.New("alternative-service and service-info are exclusive")
		}

		if d.AlternativeService.Type == "" {
			return errors.New("alternative-service without type")
		}
	}

	return nil
}

func isDecimal(s string) bool {
	whole, frac, hasFrac := strings.Cut(s, ".")
	return isDigits(whole) && (!hasFrac || isDigits(frac))
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

type inIMS3GPP struct {
	XMLName  xml.Name
	Version  *string   `xml:"version,attr"`
	Children []inChild `xml:",any"`
}

type inChild struct {
	XMLName  xml.Name
	Value    string    `xml:",chardata"`
	Children []inChild `xml:",any"`
}

func Decode(b []byte) (IMS3GPP, error) {
	if len(b) > maxDocumentSize {
		return IMS3GPP{}, fmt.Errorf("decode ims-3gpp: document of %d bytes exceeds %d", len(b), maxDocumentSize)
	}

	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.CharsetReader = charsetReader

	var doc inIMS3GPP
	if err := dec.Decode(&doc); err != nil {
		return IMS3GPP{}, fmt.Errorf("decode ims-3gpp: %w", err)
	}

	d, err := fromXML(doc)
	if err != nil {
		return IMS3GPP{}, fmt.Errorf("decode ims-3gpp: %w", err)
	}

	return d, nil
}

func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "us-ascii", "ascii", "utf-8", "utf8":
		return input, nil
	}

	return nil, fmt.Errorf("unsupported charset %q", label)
}

func fromXML(doc inIMS3GPP) (IMS3GPP, error) {
	if doc.XMLName.Local != "ims-3gpp" {
		return IMS3GPP{}, fmt.Errorf("root element is <%s>, want <ims-3gpp>", doc.XMLName.Local)
	}

	if doc.Version == nil {
		return IMS3GPP{}, errors.New("missing version attribute")
	}

	d := IMS3GPP{Version: strings.TrimSpace(*doc.Version)}
	if !isDecimal(d.Version) {
		return IMS3GPP{}, fmt.Errorf("version %q is not a decimal", d.Version)
	}

	for _, c := range doc.Children {
		switch c.XMLName.Local {
		case "alternative-service":
			if d.AlternativeService == nil {
				d.AlternativeService = alternativeService(c)
			}
		case "service-info":
			if d.ServiceInfo == "" {
				d.ServiceInfo = c.Value
			}
		}
	}

	if a := d.AlternativeService; a != nil {
		if a.Type == "" {
			return IMS3GPP{}, errors.New("alternative-service without type")
		}

		d.ServiceInfo = ""
	}

	return d, nil
}

func alternativeService(c inChild) *AlternativeService {
	a := &AlternativeService{}

	for _, e := range c.Children {
		v := strings.TrimSpace(e.Value)

		switch {
		case e.XMLName.Local == "type" && a.Type == "":
			a.Type = v
		case e.XMLName.Local == "reason" && a.Reason == "":
			a.Reason = e.Value
		case e.XMLName.Local == "action" && a.Action == "":
			a.Action = v
		}
	}

	return a
}
