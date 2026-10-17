package sip

import (
	"fmt"
	"slices"
	"strings"
)

// Option tags (RFC 3261 §19.2) of the extensions this package and its users implement.
const (
	OptionTag100rel           = "100rel"            // RFC 3262
	OptionTag199              = "199"               // RFC 6228
	OptionTagAnswerMode       = "answermode"        // RFC 5373
	OptionTagEarlySession     = "early-session"     // RFC 3959
	OptionTagExplicitSub      = "explicitsub"       // RFC 7614
	OptionTagGRUU             = "gruu"              // RFC 5627
	OptionTagMultipleRefer    = "multiple-refer"    // RFC 5368
	OptionTagNoReferSub       = "norefersub"        // RFC 4488
	OptionTagNoSub            = "nosub"             // RFC 7614
	OptionTagOutbound         = "outbound"          // RFC 5626
	OptionTagPath             = "path"              // RFC 3327
	OptionTagPrecondition     = "precondition"      // RFC 3312
	OptionTagReplaces         = "replaces"          // RFC 3891
	OptionTagResourcePriority = "resource-priority" // RFC 4412
	OptionTagSecAgree         = "sec-agree"         // RFC 3329
	OptionTagTargetDialog     = "tdialog"           // RFC 4538
	OptionTagTimer            = "timer"             // RFC 4028
)

// OptionTags are the option tags of Require, Proxy-Require, Supported or Unsupported (RFC 3261 §20).
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

// HasOptionTag reports whether a Require, Proxy-Require, Supported or Unsupported header field lists an option tag,
// which compare case-insensitively (RFC 3261 §19.2).
func (fs Header) HasOptionTag(name, tag string) bool {
	return slices.ContainsFunc(fs.Elements(name), func(v string) bool { return strings.EqualFold(v, tag) })
}
