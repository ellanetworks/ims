package n5

import (
	"encoding/json"
	"testing"
)

func audio(port string, rtcp bool, bw BitRate) MediaComponent {
	c := MediaComponent{
		MedCompN: 1, MedType: MediaAudio, FStatus: FlowEnabled, MarBwUl: &bw, MarBwDl: &bw,
		Codecs: []CodecData{"uplink\noffer\nm=audio " + port + " RTP/AVP 116\r\n"},
		MedSubComps: map[string]MediaSubComponent{
			"1": {FNum: 1, FDescs: []FlowDescription{"permit in 17 from 10.45.0.2 " + port + " to any"}},
		},
	}

	if rtcp {
		c.MedSubComps["2"] = MediaSubComponent{FNum: 2, FlowUsage: FlowUsageRTCP}
	}

	return c
}

func TestNewPatch(t *testing.T) {
	events := &EventsSubscReqData{Events: []AfEventSubscription{{Event: EventChargingCorrelation}}, NotifURI: "http://ims/n"}
	base := &AppSessionContextUpdateData{
		AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio("49000", true, 64000)},
	}

	for name, tc := range map[string]struct {
		prev, next *AppSessionContextUpdateData
		want       string
	}{
		"no previous": {
			nil, &AppSessionContextUpdateData{MedComponents: map[string]MediaComponent{"1": {MedCompN: 1, MedType: MediaAudio}}},
			`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"medType":"AUDIO"}}}}`,
		},
		"unchanged": {base, base, `{}`},
		"bandwidth keeps medCompN": {
			base, &AppSessionContextUpdateData{
				AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio("49000", true, 128000)},
			},
			`{"ascReqData":{"medComponents":{"1":{"marBwDl":"128000 bps","marBwUl":"128000 bps","medCompN":1}}}}`,
		},
		"port replaces the flows, keeps fNum": {
			base, &AppSessionContextUpdateData{
				AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio("50000", true, 64000)},
			},
			`{"ascReqData":{"medComponents":{"1":{"codecs":["uplink\noffer\nm=audio 50000 RTP/AVP 116\r\n"],"medCompN":1,` +
				`"medSubComps":{"1":{"fDescs":["permit in 17 from 10.45.0.2 50000 to any"],"fNum":1}}}}}}`,
		},
		"sub-component removed": {
			base, &AppSessionContextUpdateData{
				AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio("49000", false, 64000)},
			},
			`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"medSubComps":{"2":null}}}}}`,
		},
		"component removed, another added, subscription ended": {
			base, &AppSessionContextUpdateData{
				AFAppID: "app", MedComponents: map[string]MediaComponent{"2": {MedCompN: 2, MedType: MediaVideo}},
			},
			`{"ascReqData":{"evSubsc":null,"medComponents":{"1":null,"2":{"medCompN":2,"medType":"VIDEO"}}}}`,
		},
		"notification URI keeps events": {
			base, &AppSessionContextUpdateData{
				AFAppID: "app", EvSubsc: &EventsSubscReqData{Events: events.Events, NotifURI: "http://ims/m"},
				MedComponents: base.MedComponents,
			},
			`{"ascReqData":{"evSubsc":{"events":[{"event":"CHARGING_CORRELATION"}],"notifUri":"http://ims/m"}}}`,
		},
		"forking": {
			base, &AppSessionContextUpdateData{
				AFAppID: "app", EvSubsc: events, MedComponents: base.MedComponents, SipForkInd: ForkingSeveralDialogues,
			},
			`{"ascReqData":{"sipForkInd":"SEVERAL_DIALOGUES"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := NewPatch(tc.prev, tc.next)
			if err != nil {
				t.Fatal(err)
			}

			if p.String() != tc.want {
				t.Fatalf("patch\n%s\nwant\n%s", p, tc.want)
			}

			if p.Empty() != (tc.want == `{}`) {
				t.Fatalf("Empty() = %t for %s", p.Empty(), p)
			}

			// RFC 7396 §2: applying the patch to prev gives next.
			if tc.prev != nil {
				var prev, patch, next any

				mustRoundTrip(t, tc.prev, &prev)
				mustRoundTrip(t, p, &patch)
				mustRoundTrip(t, tc.next, &next)

				got := prev
				if d, ok := patch.(map[string]any)["ascReqData"]; ok {
					got = mergePatch(prev, d)
				}

				if g, w := mustJSON(t, got), mustJSON(t, next); g != w {
					t.Fatalf("applied patch gives\n%s\nwant\n%s", g, w)
				}
			}
		})
	}
}

func mustRoundTrip(t *testing.T, v, out any) {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// mergePatch is RFC 7396 §2 MergePatch, as the PCF applies it.
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
