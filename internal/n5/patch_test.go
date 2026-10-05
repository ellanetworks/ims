package n5

import (
	"encoding/json"
	"strings"
	"testing"
)

func audio(rtcp bool, bw BitRate) MediaComponent {
	c := MediaComponent{
		MedCompN: 1, MedType: MediaAudio, FStatus: FlowEnabled, MarBwUl: &bw, MarBwDl: &bw,
		Codecs: []CodecData{"uplink\noffer\nm=audio 49000 RTP/AVP 116\r\n"},
		MedSubComps: map[string]MediaSubComponent{
			"1": {FNum: 1, FDescs: []FlowDescription{"permit in 17 from 10.45.0.2 49000 to any"}},
		},
	}

	if rtcp {
		c.MedSubComps["2"] = MediaSubComponent{FNum: 2, FlowUsage: FlowUsageRTCP, FDescs: []FlowDescription{"permit in 17 from any to any"}}
	}

	return c
}

const audio49000 = `{"codecs":["uplink\noffer\nm=audio 49000 RTP/AVP 116\r\n"],"fStatus":"ENABLED","marBwDl":"64000 bps",` +
	`"marBwUl":"64000 bps","medCompN":1,"medSubComps":{"1":{"fDescs":["permit in 17 from 10.45.0.2 49000 to any"],"fNum":1},` +
	`"2":{"fDescs":["permit in 17 from any to any"],"fNum":2,"flowUsage":"RTCP"}},"medType":"AUDIO"}`

func TestNewPatch(t *testing.T) {
	events := &EventsSubscReqData{Events: []AfEventSubscription{{Event: EventChargingCorrelation}}, NotifURI: "http://ims/n"}
	base := &AppSessionContextUpdateData{
		AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio(true, 64000)},
	}

	with := func(edit func(*AppSessionContextUpdateData)) *AppSessionContextUpdateData {
		next := *base
		next.MedComponents = map[string]MediaComponent{"1": audio(true, 64000)}
		edit(&next)

		return &next
	}

	for name, tc := range map[string]struct {
		prev, next *AppSessionContextUpdateData
		want       string
		// The PCF ends with less than next: what cannot be removed stays (TS 29.501 §5.3.8.2).
		kept string
	}{
		"no previous": {
			prev: nil, next: &AppSessionContextUpdateData{MedComponents: map[string]MediaComponent{"1": {MedCompN: 1, MedType: MediaAudio}}},
			want: `{"ascReqData":{"medComponents":{"1":{"medCompN":1,"medType":"AUDIO"}}}}`,
		},
		"unchanged": {prev: base, next: base, want: `{}`},
		"bandwidth resends the component": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) {
				n.MedComponents["1"] = audio(true, 128000)
			}),
			want: `{"ascReqData":{"medComponents":{"1":` +
				`{"codecs":["uplink\noffer\nm=audio 49000 RTP/AVP 116\r\n"],"fStatus":"ENABLED","marBwDl":"128000 bps",` +
				`"marBwUl":"128000 bps","medCompN":1,"medSubComps":{"1":{"fDescs":["permit in 17 from 10.45.0.2 49000 to any"],"fNum":1},` +
				`"2":{"fDescs":["permit in 17 from any to any"],"fNum":2,"flowUsage":"RTCP"}},"medType":"AUDIO"}}}}`,
		},
		"sub-component and bandwidth removed": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) {
				c := audio(false, 64000)
				c.MarBwDl = nil
				n.MedComponents["1"] = c
			}),
			want: `{"ascReqData":{"medComponents":{"1":` +
				`{"codecs":["uplink\noffer\nm=audio 49000 RTP/AVP 116\r\n"],"fStatus":"ENABLED","marBwDl":null,` +
				`"marBwUl":"64000 bps","medCompN":1,"medSubComps":{"1":{"fDescs":["permit in 17 from 10.45.0.2 49000 to any"],"fNum":1},` +
				`"2":null},"medType":"AUDIO"}}}}`,
		},
		"flow usage and flows dropped": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) {
				c := audio(true, 64000)
				c.MedSubComps["2"] = MediaSubComponent{FNum: 2}
				n.MedComponents["1"] = c
			}),
			want: `{"ascReqData":{"medComponents":{"1":` +
				`{"codecs":["uplink\noffer\nm=audio 49000 RTP/AVP 116\r\n"],"fStatus":"ENABLED","marBwDl":"64000 bps",` +
				`"marBwUl":"64000 bps","medCompN":1,"medSubComps":{"1":{"fDescs":["permit in 17 from 10.45.0.2 49000 to any"],"fNum":1},` +
				`"2":{"fDescs":null,"fNum":2,"flowUsage":"NO_INFO"}},"medType":"AUDIO"}}}}`,
			kept: "flowUsage",
		},
		"media line disabled keeps codecs and flows": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) {
				n.MedComponents["1"] = MediaComponent{MedCompN: 1, MedType: MediaAudio, FStatus: FlowRemoved}
			}),
			want: `{"ascReqData":{"medComponents":{"1":{"fStatus":"REMOVED","marBwDl":null,"marBwUl":null,"medCompN":1,` +
				`"medSubComps":{"1":null,"2":null},"medType":"AUDIO"}}}}`,
			kept: "codecs",
		},
		"component removed, another added, subscription ended": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) {
				n.EvSubsc = nil
				n.MedComponents = map[string]MediaComponent{"2": {MedCompN: 2, MedType: MediaVideo}}
			}),
			want: `{"ascReqData":{"evSubsc":null,"medComponents":{"1":null,"2":{"medCompN":2,"medType":"VIDEO"}}}}`,
		},
		"notification URI resends the subscription": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) {
				n.EvSubsc = &EventsSubscReqData{Events: events.Events, NotifURI: "http://ims/m"}
			}),
			want: `{"ascReqData":{"evSubsc":{"events":[{"event":"CHARGING_CORRELATION"}],"notifUri":"http://ims/m"}}}`,
		},
		"forking resends every component": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) { n.SipForkInd = ForkingSeveralDialogues }),
			want: `{"ascReqData":{"medComponents":{"1":` + audio49000 + `},"sipForkInd":"SEVERAL_DIALOGUES"}}`,
		},
		// Annex B.3.1: every update within the forked early dialogues says SEVERAL_DIALOGUES.
		"forking goes on": {
			prev: with(func(n *AppSessionContextUpdateData) { n.SipForkInd = ForkingSeveralDialogues }),
			next: with(func(n *AppSessionContextUpdateData) { n.SipForkInd = ForkingSeveralDialogues }),
			want: `{"ascReqData":{"medComponents":{"1":` + audio49000 + `},"sipForkInd":"SEVERAL_DIALOGUES"}}`,
		},
		"forking ends": {
			prev: with(func(n *AppSessionContextUpdateData) { n.SipForkInd = ForkingSeveralDialogues }), next: base,
			want: `{"ascReqData":{"medComponents":{"1":` + audio49000 + `},"sipForkInd":null}}`,
		},
		"application identifier cannot be removed": {
			prev: base, next: with(func(n *AppSessionContextUpdateData) { n.AFAppID = "" }),
			want: `{}`, kept: "afAppId",
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

			if err := CheckPatch(p.body); err != nil {
				t.Fatalf("%s: %v", p, err)
			}

			if tc.prev == nil || tc.kept != "" {
				return
			}

			// RFC 7396 §2: applying the patch to prev gives next.
			prev, _ := json.Marshal(tc.prev)

			var patch struct {
				AscReqData json.RawMessage `json:"ascReqData"`
			}

			_ = json.Unmarshal(p.body, &patch)

			got := prev
			if patch.AscReqData != nil {
				if got, err = ApplyPatch(prev, patch.AscReqData); err != nil {
					t.Fatal(err)
				}
			}

			want, _ := json.Marshal(tc.next)
			sameJSON(t, got, want)
		})
	}
}

// After an unanswered PATCH, the PCF holds the state before or after it: the patch fits both.
func TestNewResyncPatch(t *testing.T) {
	events := &EventsSubscReqData{Events: []AfEventSubscription{{Event: EventChargingCorrelation}}, NotifURI: "http://ims/n"}
	before := &AppSessionContextUpdateData{AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio(false, 64000)}}
	tried := &AppSessionContextUpdateData{
		AFAppID: "app", EvSubsc: events, SipForkInd: ForkingSeveralDialogues,
		MedComponents: map[string]MediaComponent{"1": audio(true, 64000), "2": {MedCompN: 2, MedType: MediaVideo}},
	}
	next := &AppSessionContextUpdateData{AFAppID: "app", EvSubsc: events, MedComponents: map[string]MediaComponent{"1": audio(false, 64000)}}

	p, err := NewResyncPatch(next, tried, before)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"ascReqData":{"afAppId":"app","evSubsc":{"events":[{"event":"CHARGING_CORRELATION"}],"notifUri":"http://ims/n"},` +
		`"medComponents":{"1":{"codecs":["uplink\noffer\nm=audio 49000 RTP/AVP 116\r\n"],"fStatus":"ENABLED","marBwDl":"64000 bps",` +
		`"marBwUl":"64000 bps","medCompN":1,"medSubComps":{"1":{"fDescs":["permit in 17 from 10.45.0.2 49000 to any"],"fNum":1},` +
		`"2":null},"medType":"AUDIO"},"2":null},"sipForkInd":null}}`
	if p.String() != want {
		t.Fatalf("patch\n%s\nwant\n%s", p, want)
	}

	if err := CheckPatch(p.body); err != nil {
		t.Fatal(err)
	}

	var body struct {
		AscReqData json.RawMessage `json:"ascReqData"`
	}

	_ = json.Unmarshal(p.body, &body)

	for _, held := range []*AppSessionContextUpdateData{before, tried} {
		doc, _ := json.Marshal(held)

		got, err := ApplyPatch(doc, body.AscReqData)
		if err != nil {
			t.Fatal(err)
		}

		w, _ := json.Marshal(next)
		sameJSON(t, got, w)
	}

	// Nothing held: next in full.
	if p, err := NewResyncPatch(next); err != nil || !strings.Contains(p.String(), `"afAppId":"app"`) {
		t.Fatalf("NewResyncPatch(next) = %s, %v", p, err)
	}
}

func TestNewPatchWithoutEvents(t *testing.T) {
	if _, err := NewPatch(nil, &AppSessionContextUpdateData{EvSubsc: &EventsSubscReqData{}}); err == nil {
		t.Fatal("an events subscription without events accepted")
	}
}

// TS 29.501 §5.3.8.2, TS 29.514 §5.6.2.25-27
func TestCheckPatch(t *testing.T) {
	for body, ok := range map[string]bool{
		`{}`: true,
		`{"ascReqData":{"evSubsc":null,"sipForkInd":null,"medComponents":{"1":null}}}`:                       true,
		`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"rrBw":null,"medSubComps":{"2":null}}}}}`:         true,
		`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"medSubComps":{"2":{"fNum":2,"fDescs":null}}}}}}`: true,
		`{"ascReqData":{"afAppId":null}}`:                                                                false,
		`{"ascReqData":{"medComponents":null}}`:                                                          false,
		`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"codecs":null}}}}`:                            false,
		`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"medSubComps":null}}}}`:                       false,
		`{"ascReqData":{"medComponents":{"1":{"marBwUl":"1 bps"}}}}`:                                     false,
		`{"ascReqData":{"medComponents":{"1":{"medCompN":1,"medSubComps":{"2":{"flowUsage":"RTCP"}}}}}}`: false,
		`{"ascReqData":{"evSubsc":{"notifUri":"http://ims/n"}}}`:                                         false,
		`{"ascReqData":{"evSubsc":{"events":null}}}`:                                                     false,
	} {
		if err := CheckPatch([]byte(body)); (err == nil) != ok {
			t.Errorf("%s: %v, want ok %t", body, err, ok)
		}
	}
}

func TestApplyPatch(t *testing.T) {
	got, err := ApplyPatch([]byte(`{"a":{"b":1,"c":[1]},"d":2}`), []byte(`{"a":{"b":null,"c":[2],"e":{"f":null}},"d":null}`))
	if err != nil {
		t.Fatal(err)
	}

	sameJSON(t, got, []byte(`{"a":{"c":[2],"e":{}}}`))
}
