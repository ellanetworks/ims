package n5

import (
	"encoding/json"
	"net/netip"
)

// AppSessionContext is the Individual Application Session Context resource (TS 29.514 §5.6.2.2).
type AppSessionContext struct {
	AscReqData  *AppSessionContextReqData  `json:"ascReqData,omitempty"`
	AscRespData *AppSessionContextRespData `json:"ascRespData,omitempty"`
	EvsNotif    *EventsNotification        `json:"evsNotif,omitempty"`
}

// TS 29.514 §5.6.2.3. NotifURI and SuppFeat are required, and so is one of UEIPv4 and UEIPv6.
type AppSessionContextReqData struct {
	AFAppID       string                    `json:"afAppId,omitempty"`
	AFChargID     string                    `json:"afChargId,omitempty"`
	DNN           string                    `json:"dnn,omitempty"`
	EvSubsc       *EventsSubscReqData       `json:"evSubsc,omitempty"`
	MedComponents map[string]MediaComponent `json:"medComponents,omitempty"`
	NotifURI      string                    `json:"notifUri"`
	ServURN       string                    `json:"servUrn,omitempty"`
	SliceInfo     *Snssai                   `json:"sliceInfo,omitempty"`
	SUPI          string                    `json:"supi,omitempty"`
	GPSI          string                    `json:"gpsi,omitempty"`
	SuppFeat      SupportedFeatures         `json:"suppFeat"`
	UEIPv4        netip.Addr                `json:"ueIpv4,omitzero"`
	UEIPv6        netip.Addr                `json:"ueIpv6,omitzero"`
}

// TS 29.514 §5.6.2.4
type AppSessionContextRespData struct {
	SuppFeat SupportedFeatures `json:"suppFeat,omitempty"`
}

// AppSessionContextUpdateData carries the service information of a PATCH (TS 29.514 §5.6.2.5). Its media
// components and event subscription are the nullable MediaComponentRm and EventsSubscReqDataRm; NewPatch writes
// the nulls that remove what an earlier request set.
type AppSessionContextUpdateData struct {
	AFAppID       string                    `json:"afAppId,omitempty"`
	EvSubsc       *EventsSubscReqData       `json:"evSubsc,omitempty"`
	MedComponents map[string]MediaComponent `json:"medComponents,omitempty"`
	SipForkInd    SipForkingIndication      `json:"sipForkInd,omitempty"`
}

// TS 29.514 §5.6.2.43
type AppSessionContextUpdateDataPatch struct {
	AscReqData *AppSessionContextUpdateData `json:"ascReqData,omitempty"`
}

// MediaComponent is a media component (TS 29.514 §5.6.2.7) or, in a PATCH, its MediaComponentRm form (§5.6.2.26).
// The map of sub-components is keyed by their FNum, as decimal.
type MediaComponent struct {
	Codecs      []CodecData                  `json:"codecs,omitempty"`
	FStatus     FlowStatus                   `json:"fStatus,omitempty"`
	MarBwDl     *BitRate                     `json:"marBwDl,omitempty"`
	MarBwUl     *BitRate                     `json:"marBwUl,omitempty"`
	MedCompN    uint32                       `json:"medCompN"`
	MedSubComps map[string]MediaSubComponent `json:"medSubComps,omitempty"`
	MedType     MediaType                    `json:"medType,omitempty"`
	RRBw        *BitRate                     `json:"rrBw,omitempty"`
	RSBw        *BitRate                     `json:"rsBw,omitempty"`
}

// MediaSubComponent is a media sub-component (TS 29.514 §5.6.2.8) or its MediaSubComponentRm form (§5.6.2.27).
type MediaSubComponent struct {
	FNum      uint32            `json:"fNum"`
	FDescs    []FlowDescription `json:"fDescs,omitempty"`
	FStatus   FlowStatus        `json:"fStatus,omitempty"`
	FlowUsage FlowUsage         `json:"flowUsage,omitempty"`
}

// FlowDescription is an IPFilterRule as in the Flow-Description AVP (TS 29.514 §5.6.3.2, TS 29.214 §5.3.8).
type FlowDescription = string

// CodecData is the codec information of the Codec-Data AVP (TS 29.514 §5.6.3.2, TS 29.214 §5.3.7).
type CodecData = string

// EventsSubscReqData is an events subscription (TS 29.514 §5.6.2.6) or its EventsSubscReqDataRm form (§5.6.2.25).
// Notifications go to NotifURI + "/notify".
type EventsSubscReqData struct {
	Events   []AfEventSubscription `json:"events"`
	NotifURI string                `json:"notifUri,omitempty"`
}

// TS 29.514 §5.6.2.10
type AfEventSubscription struct {
	Event       AfEvent       `json:"event"`
	NotifMethod AfNotifMethod `json:"notifMethod,omitempty"`
}

// EventsNotification is the body of a notify request (TS 29.514 §5.6.2.9), and the evsNotif of a response.
type EventsNotification struct {
	AccessType                string                        `json:"accessType,omitempty"`
	AnChargAddr               *AccNetChargingAddress        `json:"anChargAddr,omitempty"`
	AnChargIDs                []AccessNetChargingIdentifier `json:"anChargIds,omitempty"`
	AnGwAddr                  *AnGwAddress                  `json:"anGwAddr,omitempty"`
	EvSubsURI                 string                        `json:"evSubsUri"`
	EvNotifs                  []AfEventNotification         `json:"evNotifs"`
	FailedResourcAllocReports []ResourcesAllocationInfo     `json:"failedResourcAllocReports,omitempty"`
	SuccResourcAllocReports   []ResourcesAllocationInfo     `json:"succResourcAllocReports,omitempty"`
	PLMNID                    *PlmnIDNid                    `json:"plmnId,omitempty"`
	QncReports                []QosNotificationControlInfo  `json:"qncReports,omitempty"`
	RanNasRelCauses           []json.RawMessage             `json:"ranNasRelCauses,omitempty"`
	RatType                   string                        `json:"ratType,omitempty"`
	UELoc                     json.RawMessage               `json:"ueLoc,omitempty"`
	UELocTime                 string                        `json:"ueLocTime,omitempty"`
	UETimeZone                string                        `json:"ueTimeZone,omitempty"`
}

// TS 29.514 §5.6.2.11
type AfEventNotification struct {
	Event      AfEvent `json:"event"`
	Flows      []Flows `json:"flows,omitempty"`
	RetryAfter *uint32 `json:"retryAfter,omitempty"`
}

// Flows names the flows of one media component, all of them when FNums is empty (TS 29.514 §5.6.2.21).
type Flows struct {
	FNums    []uint32 `json:"fNums,omitempty"`
	MedCompN uint32   `json:"medCompN"`
}

// TS 29.514 §5.6.2.14
type ResourcesAllocationInfo struct {
	McResourcStatus MediaComponentResourcesStatus `json:"mcResourcStatus,omitempty"`
	Flows           []Flows                       `json:"flows,omitempty"`
	AltSerReq       string                        `json:"altSerReq,omitempty"`
}

// TS 29.514 §5.6.2.15
type QosNotificationControlInfo struct {
	NotifType QosNotifType `json:"notifType"`
	Flows     []Flows      `json:"flows,omitempty"`
	AltSerReq string       `json:"altSerReq,omitempty"`
}

// TS 29.571 §5.4.4.2
type Snssai struct {
	SST uint8  `json:"sst"`
	SD  string `json:"sd,omitempty"`
}

// TS 29.571 §5.4.4.33
type PlmnIDNid struct {
	MCC string `json:"mcc"`
	MNC string `json:"mnc"`
	NID string `json:"nid,omitempty"`
}

// AccessNetChargingIdentifier has one of AccNetChargIDString and the deprecated AccNetChaIDValue
// (TS 29.514 §5.6.2.32).
type AccessNetChargingIdentifier struct {
	AccNetChaIDValue    *uint32 `json:"accNetChaIdValue,omitempty"`
	AccNetChargIDString string  `json:"accNetChargIdString,omitempty"`
	Flows               []Flows `json:"flows,omitempty"`
}

// TS 29.512 (AccNetChargingAddress)
type AccNetChargingAddress struct {
	AnChargIPv4Addr netip.Addr `json:"anChargIpv4Addr,omitzero"`
	AnChargIPv6Addr netip.Addr `json:"anChargIpv6Addr,omitzero"`
}

// TS 29.514 §5.6.2.20
type AnGwAddress struct {
	AnGwIPv4Addr netip.Addr `json:"anGwIpv4Addr,omitzero"`
	AnGwIPv6Addr netip.Addr `json:"anGwIpv6Addr,omitzero"`
}

// TerminationInfo is the body of a terminate request (TS 29.514 §5.6.2.12).
type TerminationInfo struct {
	TermCause TerminationCause `json:"termCause"`
	ResURI    string           `json:"resUri"`
}

// ProblemDetails is an error body (TS 29.571 §5.2.4.1), including the acceptableServInfo of the
// ExtendedProblemDetails of TS 29.514 §5.6.2.29.
type ProblemDetails struct {
	Type              string            `json:"type,omitempty"`
	Title             string            `json:"title,omitempty"`
	Status            int               `json:"status,omitempty"`
	Detail            string            `json:"detail,omitempty"`
	Instance          string            `json:"instance,omitempty"`
	Cause             string            `json:"cause,omitempty"`
	InvalidParams     []InvalidParam    `json:"invalidParams,omitempty"`
	SupportedFeatures SupportedFeatures `json:"supportedFeatures,omitempty"`

	AcceptableServInfo *AcceptableServiceInfo `json:"acceptableServInfo,omitempty"`
}

// AcceptableServiceInfo is the service information the PCF would accept, in a 403 (TS 29.514 §5.6.2.30).
type AcceptableServiceInfo struct {
	AccBwMedComps map[string]MediaComponent `json:"accBwMedComps,omitempty"`
	MarBwUl       *BitRate                  `json:"marBwUl,omitempty"`
	MarBwDl       *BitRate                  `json:"marBwDl,omitempty"`
}

// TS 29.571 §5.2.4.6
type InvalidParam struct {
	Param  string `json:"param"`
	Reason string `json:"reason,omitempty"`
}

// Enumerations are strings: TS 29.501 §5.3.12 lets a peer send values this release does not define.

// TS 29.514 §5.6.3.7
type AfEvent string

const (
	EventAccessTypeChange              AfEvent = "ACCESS_TYPE_CHANGE"
	EventANIReport                     AfEvent = "ANI_REPORT"
	EventChargingCorrelation           AfEvent = "CHARGING_CORRELATION"
	EventFailedResourcesAllocation     AfEvent = "FAILED_RESOURCES_ALLOCATION"
	EventPLMNChange                    AfEvent = "PLMN_CHG"
	EventQoSNotif                      AfEvent = "QOS_NOTIF"
	EventRANNASCause                   AfEvent = "RAN_NAS_CAUSE"
	EventSuccessfulResourcesAllocation AfEvent = "SUCCESSFUL_RESOURCES_ALLOCATION"
)

// TS 29.514 §5.6.3.8
type AfNotifMethod string

const (
	NotifEventDetection AfNotifMethod = "EVENT_DETECTION"
	NotifOneTime        AfNotifMethod = "ONE_TIME"
	NotifPeriodic       AfNotifMethod = "PERIODIC"
)

// TS 29.514 §5.6.3.12
type FlowStatus string

const (
	FlowEnabledUplink   FlowStatus = "ENABLED-UPLINK"
	FlowEnabledDownlink FlowStatus = "ENABLED-DOWNLINK"
	FlowEnabled         FlowStatus = "ENABLED"
	FlowDisabled        FlowStatus = "DISABLED"
	FlowRemoved         FlowStatus = "REMOVED"
)

// TS 29.514 §5.6.3.3
type MediaType string

const (
	MediaAudio       MediaType = "AUDIO"
	MediaVideo       MediaType = "VIDEO"
	MediaData        MediaType = "DATA"
	MediaApplication MediaType = "APPLICATION"
	MediaControl     MediaType = "CONTROL"
	MediaText        MediaType = "TEXT"
	MediaMessage     MediaType = "MESSAGE"
	MediaOther       MediaType = "OTHER"
)

// TS 29.514 §5.6.3.14
type FlowUsage string

const (
	FlowUsageNoInfo       FlowUsage = "NO_INFO"
	FlowUsageRTCP         FlowUsage = "RTCP"
	FlowUsageAFSignalling FlowUsage = "AF_SIGNALLING"
)

// TS 29.514 §5.6.3.10
type TerminationCause string

const (
	TerminationAllSDFDeactivation            TerminationCause = "ALL_SDF_DEACTIVATION"
	TerminationPDUSessionTermination         TerminationCause = "PDU_SESSION_TERMINATION"
	TerminationPSToCSHO                      TerminationCause = "PS_TO_CS_HO"
	TerminationInsufficientServerResources   TerminationCause = "INSUFFICIENT_SERVER_RESOURCES"
	TerminationInsufficientQoSFlowResources  TerminationCause = "INSUFFICIENT_QOS_FLOW_RESOURCES"
	TerminationSponsoredDataDisallowed       TerminationCause = "SPONSORED_DATA_CONNECTIVITY_DISALLOWED"
	TerminationRequestQoSNotSupportedInPLMN  TerminationCause = "REQUEST_QOS_NOT_SUPPORTED_IN_PLMN"
	TerminationUEAddrRelease                 TerminationCause = "UE_ADDR_RELEASE"
	TerminationSMFFailure                    TerminationCause = "SMF_FAILURE"
	TerminationReflectiveQoSNotSupportedInUE TerminationCause = "REFLECTIVE_QOS_NOT_SUPPORTED_IN_UE"
)

// TS 29.514 §5.6.3.9
type QosNotifType string

const (
	QoSGuaranteed      QosNotifType = "GUARANTEED"
	QoSNotGuaranteed   QosNotifType = "NOT_GUARANTEED"
	QoSNotGuaranteedDL QosNotifType = "NOT_GUARANTEED_DL"
	QoSNotGuaranteedUL QosNotifType = "NOT_GUARANTEED_UL"
)

// TS 29.514 §5.6.3.17
type SipForkingIndication string

const (
	ForkingSingleDialogue   SipForkingIndication = "SINGLE_DIALOGUE"
	ForkingSeveralDialogues SipForkingIndication = "SEVERAL_DIALOGUES"
)

// TS 29.514 §5.6.3.13
type MediaComponentResourcesStatus string

const (
	ResourcesActive   MediaComponentResourcesStatus = "ACTIVE"
	ResourcesInactive MediaComponentResourcesStatus = "INACTIVE"
)
