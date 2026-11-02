package sip

import (
	"fmt"
	"slices"
)

// RFC 6086 section 7.2
type InfoPackage struct {
	Name   string
	Params Params
}

func ParseInfoPackage(s string) (InfoPackage, error) {
	name, ps, err := ParseTokenParams(s)
	if err != nil {
		return InfoPackage{}, fmt.Errorf("info-package-type: %w", err)
	}

	return InfoPackage{Name: name, Params: ps}, nil
}

func (p InfoPackage) String() string {
	return p.Name + p.Params.String()
}

// RFC 6086 section 4.2.1
func (fs Header) InfoPackage() (InfoPackage, bool, error) {
	v, ok, err := fs.single("Info-Package")
	if !ok || err != nil {
		return InfoPackage{}, ok, err
	}

	p, err := ParseInfoPackage(v)

	return p, true, err
}

// RFC 6086 section 5.2.2
func (fs Header) RecvInfo() ([]InfoPackage, bool, error) {
	if !fs.Has("Recv-Info") {
		return nil, false, nil
	}

	var out []InfoPackage

	for _, v := range fs.Elements("Recv-Info") {
		p, err := ParseInfoPackage(v)
		if err != nil {
			return nil, true, fmt.Errorf("Recv-Info: %w", err)
		}

		out = append(out, p)
	}

	return out, true, nil
}

// RFC 6086 section 7.2
func AcceptsInfo(recv []InfoPackage, name string) bool {
	return slices.ContainsFunc(recv, func(p InfoPackage) bool { return p.Name == name })
}
