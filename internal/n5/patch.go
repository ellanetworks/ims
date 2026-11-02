package n5

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// TS 29.514 clause 4.2.3.2
// RFC 7396
type Patch struct {
	body []byte
}

// TS 29.513 clause 7.2.3
// TS 29.514 clause B.3
// TS 29.514 clause B.3.1
// TS 29.514 clause B.3.2
// TS 29.501 clause 5.3.8.2
func NewPatch(prev, next *AppSessionContextUpdateData) (Patch, error) {
	from, to, err := trees(next, prev)
	if err != nil {
		return Patch{}, err
	}

	changed := !reflect.DeepEqual(from["sipForkInd"], to["sipForkInd"])

	forking := to["sipForkInd"] == string(ForkingSeveralDialogues)
	if forking {
		delete(from, "sipForkInd")
	}

	return patch(from, to, forking || changed, false)
}

// TS 29.500 clause 5.2.7.2
func NewResyncPatch(next *AppSessionContextUpdateData, held ...*AppSessionContextUpdateData) (Patch, error) {
	from, to, err := trees(next, held...)
	if err != nil {
		return Patch{}, err
	}

	return patch(from, to, true, true)
}

func trees(next *AppSessionContextUpdateData, held ...*AppSessionContextUpdateData) (from, to map[string]any, err error) {
	if next != nil && next.EvSubsc != nil && len(next.EvSubsc.Events) == 0 {
		return nil, nil, errors.New("patch: events subscription without events")
	}

	if to, err = tree(next); err != nil {
		return nil, nil, err
	}

	from = map[string]any{}

	for _, h := range held {
		t, err := tree(h)
		if err != nil {
			return nil, nil, err
		}

		from = object(union(from, t))
	}

	return from, to, nil
}

func union(a, b any) any {
	x, okA := a.(map[string]any)
	y, okB := b.(map[string]any)

	if !okA || !okB {
		return b
	}

	out := make(map[string]any, len(x)+len(y))

	for k, v := range x {
		out[k] = v
	}

	for k, v := range y {
		if old, ok := out[k]; ok {
			out[k] = union(old, v)
		} else {
			out[k] = v
		}
	}

	return out
}

func patch(from, to map[string]any, all, every bool) (Patch, error) {
	diff := map[string]any{}

	for k, v := range to {
		if old, ok := from[k]; k != "medComponents" && (every || !ok || !reflect.DeepEqual(old, v)) {
			diff[k] = v
		}
	}

	for _, k := range []string{"evSubsc", "sipForkInd"} {
		if _, ok := to[k]; !ok && from[k] != nil {
			diff[k] = nil
		}
	}

	if c := components(object(from["medComponents"]), object(to["medComponents"]), all); len(c) > 0 {
		diff["medComponents"] = c
	}

	body := map[string]any{}
	if len(diff) > 0 {
		body["ascReqData"] = diff
	}

	b, err := json.Marshal(body)

	return Patch{body: b}, err
}

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

// TS 29.514 clause 5.6.2.26
// TS 29.514 clause 5.6.2.27
var (
	nullableComponent    = []string{"marBwDl", "marBwUl", "mirBwDl", "mirBwUl", "rrBw", "rsBw"}
	nullableSubComponent = []string{"fDescs", "marBwDl", "marBwUl"}
)

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

// RFC 7396 section 2
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

// TS 29.501 clause 5.3.8.2
// TS 29.514 clause 5.6.2.25
// TS 29.514 clause 5.6.2.26
// TS 29.514 clause 5.6.2.27
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
