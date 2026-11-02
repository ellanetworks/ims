package sip

import (
	"fmt"
	"slices"
	"strings"
)

// RFC 3261 section 19.2
const (
	OptionTag100rel           = "100rel"
	OptionTag199              = "199"
	OptionTagAnswerMode       = "answermode"
	OptionTagEarlySession     = "early-session"
	OptionTagExplicitSub      = "explicitsub"
	OptionTagGRUU             = "gruu"
	OptionTagMultipleRefer    = "multiple-refer"
	OptionTagNoReferSub       = "norefersub"
	OptionTagNoSub            = "nosub"
	OptionTagOutbound         = "outbound"
	OptionTagPath             = "path"
	OptionTagPrecondition     = "precondition"
	OptionTagReplaces         = "replaces"
	OptionTagResourcePriority = "resource-priority"
	OptionTagSecAgree         = "sec-agree"
	OptionTagTargetDialog     = "tdialog"
	OptionTagTimer            = "timer"
)

// RFC 3261 section 20
func (fs Header) OptionTags(name string) ([]string, error) {
	var out []string

	for _, v := range fs.Elements(name) {
		if !isToken(v) {
			return nil, fmt.Errorf("%s: invalid option tag %q", name, v)
		}

		out = append(out, v)
	}

	return out, nil
}

// RFC 3261 section 19.2
func (fs Header) HasOptionTag(name, tag string) bool {
	return slices.ContainsFunc(fs.Elements(name), func(v string) bool { return strings.EqualFold(v, tag) })
}
