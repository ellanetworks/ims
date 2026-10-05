package n5

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// Patch is the content of a PATCH request: an AppSessionContextUpdateDataPatch encoded as a JSON merge patch
// (TS 29.514 §4.2.3.2, RFC 7396).
type Patch struct {
	body []byte
}

// NewPatch returns the merge patch that turns the service information the PCF holds, prev, into next.
//
//   - A media component that changes is sent in full, with its sub-components: the P-CSCF derives each media
//     component from the whole SDP exchange (TS 29.513 §7.2.3), and TS 29.514 Annex B.3 asks for the full
//     service information.
//   - When sipForkInd changes, every media component is sent (Annex B.3.1, B.3.2).
//   - What prev has and next lacks is set to null where the schema lets it be removed (TS 29.501 §5.3.8.2): a
//     media component, a sub-component, a bit rate, flow descriptions, the events subscription, and sipForkInd
//     (Annex B.3.2). A flow usage falls back to NO_INFO. Anything else keeps its previous value at the PCF.
//
// A nil prev patches only what next sets.
func NewPatch(prev, next *AppSessionContextUpdateData) (Patch, error) {
	if next != nil && next.EvSubsc != nil && len(next.EvSubsc.Events) == 0 {
		return Patch{}, errors.New("patch: events subscription without events")
	}

	from, err := tree(prev)
	if err != nil {
		return Patch{}, err
	}

	to, err := tree(next)
	if err != nil {
		return Patch{}, err
	}

	forking := !reflect.DeepEqual(from["sipForkInd"], to["sipForkInd"])
	diff := map[string]any{}

	for k, v := range to {
		if old, ok := from[k]; k != "medComponents" && (!ok || !reflect.DeepEqual(old, v)) {
			diff[k] = v
		}
	}

	for _, k := range []string{"evSubsc", "sipForkInd"} {
		if _, ok := to[k]; !ok && from[k] != nil {
			diff[k] = nil
		}
	}

	if c := components(object(from["medComponents"]), object(to["medComponents"]), forking); len(c) > 0 {
		diff["medComponents"] = c
	}

	body := map[string]any{}
	if len(diff) > 0 {
		body["ascReqData"] = diff
	}

	b, err := json.Marshal(body)

	return Patch{body: b}, err
}

// Empty reports whether the patch changes nothing.
func (p Patch) Empty() bool {
	return len(p.body) == 0 || bytes.Equal(p.body, []byte("{}"))
}

func (p Patch) MarshalJSON() ([]byte, error) {
	if len(p.body) == 0 {
		return []byte("{}"), nil
	}

	return p.body, nil
}

func (p Patch) String() string {
	b, _ := p.MarshalJSON()
	return string(b)
}

func tree(v *AppSessionContextUpdateData) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}

	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var m map[string]any

	return m, json.Unmarshal(b, &m)
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// TS 29.514 §5.6.2.26, §5.6.2.27: the attributes of the Rm types this package writes that may be null.
var (
	nullableComponent    = []string{"marBwDl", "marBwUl", "mirBwDl", "mirBwUl", "rrBw", "rsBw"}
	nullableSubComponent = []string{"fDescs", "marBwDl", "marBwUl"}
)

// components returns the medComponents of a patch: removed components as null, and changed ones (all of them
// when all is set) in full.
func components(from, to map[string]any, all bool) map[string]any {
	out := map[string]any{}

	for n := range from {
		if _, ok := to[n]; !ok {
			out[n] = nil
		}
	}

	for n, v := range to {
		if !all && reflect.DeepEqual(from[n], v) {
			continue
		}

		c := object(v)
		old := object(from[n])

		for _, k := range nullableComponent {
			removed(c, old, k)
		}

		subs := object(c["medSubComps"])
		oldSubs := object(old["medSubComps"])

		for f, s := range oldSubs {
			sub, ok := subs[f]
			if !ok {
				if subs == nil {
					subs = map[string]any{}
					c["medSubComps"] = subs
				}

				subs[f] = nil

				continue
			}

			for _, k := range nullableSubComponent {
				removed(object(sub), object(s), k)
			}

			if _, ok := object(sub)["flowUsage"]; !ok && object(s)["flowUsage"] != nil {
				object(sub)["flowUsage"] = string(FlowUsageNoInfo)
			}
		}

		out[n] = c
	}

	return out
}

func removed(next, prev map[string]any, k string) {
	if _, ok := next[k]; !ok && prev[k] != nil {
		next[k] = nil
	}
}

// ApplyPatch applies a merge patch to a JSON document (RFC 7396 §2), as the PCF does.
func ApplyPatch(doc, patch []byte) ([]byte, error) {
	var target, p any

	if len(bytes.TrimSpace(doc)) > 0 {
		if err := json.Unmarshal(doc, &target); err != nil {
			return nil, err
		}
	}

	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}

	return json.Marshal(mergePatch(target, p))
}

func mergePatch(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}

	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	}

	for k, v := range p {
		if v == nil {
			delete(t, k)
		} else {
			t[k] = mergePatch(t[k], v)
		}
	}

	return t
}

// CheckPatch checks an AppSessionContextUpdateDataPatch for what a merge patch can break in the schema: a null
// for an attribute that may not be removed (TS 29.501 §5.3.8.2), or a changed object without its required
// attributes (TS 29.514 §5.6.2.25 EventsSubscReqDataRm, §5.6.2.26 MediaComponentRm, §5.6.2.27
// MediaSubComponentRm). It knows the attributes this package writes; others pass.
func CheckPatch(patch []byte) error {
	var p struct {
		AscReqData map[string]any `json:"ascReqData"`
	}

	if err := json.Unmarshal(patch, &p); err != nil {
		return err
	}

	return check(nil, p.AscReqData)
}

func check(path []string, obj map[string]any) error {
	for _, k := range required(path) {
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("patch: %s without %s", pathString(path), k)
		}
	}

	for k, v := range obj {
		p := append(path[:len(path):len(path)], k)

		if v == nil {
			if !nullable(p) {
				return fmt.Errorf("patch: %s may not be null", pathString(p))
			}

			continue
		}

		if m, ok := v.(map[string]any); ok {
			if err := check(p, m); err != nil {
				return err
			}
		}
	}

	return nil
}

func pathString(p []string) string {
	b, _ := json.Marshal(append([]string{"ascReqData"}, p...))
	return string(b)
}

// required returns the attributes an object needs at path, below ascReqData.
func required(path []string) []string {
	switch {
	case len(path) == 1 && path[0] == "evSubsc":
		return []string{"events"}
	case len(path) == 2 && path[0] == "medComponents":
		return []string{"medCompN"}
	case len(path) == 4 && path[0] == "medComponents" && path[2] == "medSubComps":
		return []string{"fNum"}
	}

	return nil
}

// nullable reports whether the attribute at path, below ascReqData, may be set to null.
func nullable(path []string) bool {
	switch {
	case len(path) == 1:
		return path[0] == "evSubsc" || path[0] == "sipForkInd"
	case len(path) == 2:
		return path[0] == "medComponents"
	case len(path) == 3 && path[0] == "medComponents":
		return slices.Contains(nullableComponent, path[2])
	case len(path) == 4 && path[0] == "medComponents":
		return path[2] == "medSubComps"
	case len(path) == 5 && path[0] == "medComponents" && path[2] == "medSubComps":
		return slices.Contains(nullableSubComponent, path[4])
	}

	return false
}
