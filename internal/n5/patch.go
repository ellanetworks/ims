package n5

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Patch is the content of a PATCH request: an AppSessionContextUpdateDataPatch encoded as a JSON merge patch
// (TS 29.514 §4.2.3.2, RFC 7396).
type Patch struct {
	body []byte
}

// NewPatch returns the merge patch that turns the service information the PCF holds, prev, into next. What prev
// has and next lacks is set to null, so that the PCF removes it: a media component, a sub-component, a bandwidth,
// or the events subscription (TS 29.514 §4.2.3.2). Lists such as codecs, flow descriptions and events are
// replaced as a whole. A nil prev patches only what next sets.
func NewPatch(prev, next *AppSessionContextUpdateData) (Patch, error) {
	from, err := tree(prev)
	if err != nil {
		return Patch{}, err
	}

	to, err := tree(next)
	if err != nil {
		return Patch{}, err
	}

	diff := map[string]any{}
	if d := mergeDiff(nil, from, to); len(d) > 0 {
		diff["ascReqData"] = d
	}

	body, err := json.Marshal(diff)

	return Patch{body: body}, err
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

// mergeDiff returns the RFC 7396 patch from one JSON object to another, neither holding null values. An object
// that changes keeps the attributes its schema requires (TS 29.514 §5.6.2.25 EventsSubscReqDataRm, §5.6.2.26
// MediaComponentRm, §5.6.2.27 MediaSubComponentRm), even when they do not change.
func mergeDiff(path []string, from, to map[string]any) map[string]any {
	diff := map[string]any{}

	for k := range from {
		if _, ok := to[k]; !ok {
			diff[k] = nil
		}
	}

	for k, v := range to {
		old, ok := from[k]

		switch {
		case !ok:
			diff[k] = v
		case reflect.DeepEqual(old, v):
		default:
			oldObj, isObj := old.(map[string]any)
			newObj, nowObj := v.(map[string]any)

			if isObj && nowObj {
				if d := mergeDiff(append(path[:len(path):len(path)], k), oldObj, newObj); len(d) > 0 {
					diff[k] = d
				}

				continue
			}

			diff[k] = v
		}
	}

	if len(diff) > 0 {
		for _, k := range required(path) {
			if v, ok := to[k]; ok {
				if _, set := diff[k]; !set {
					diff[k] = v
				}
			}
		}
	}

	return diff
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
