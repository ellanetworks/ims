SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKRKPK2CBVE63RKQAQZJR2PM3P26,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK6HRYFHCINZWUDCA442CQ2OEO7K,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK4H4NINH7MAMQEOMFANXMAIW7HV,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKA4A6QC3D7E3AFZ2SNOL25MAIDY,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKFZ55LYRTIXQD3CYUYYB55R6DDR,SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKNpFx0WSmhe5c2kG;received=10.46.0.17;rport=57130
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=lqrC6ZCHZP
To: <tel:+15550000003>;tag=6Zi2atuHsl
Call-ID: I20FO7fKKvBIvQMv2eVJjemL
Session-ID: ceb30349f3222f86ed022861c59032f8
CSeq: 1 INVITE
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=YJ4WZXIYMBHBE7K6;lr;r2=on;ue>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=YJ4WZXIYMBHBE7K6;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=SYCHFOIZPH4EIYC7;lr>, <sip:mo@10.80.0.5:5080;did=NSSPSE7GI5UD2PLW;lr>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=G72XAUFBKDXPVFBV;lr;r2=on>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=G72XAUFBKDXPVFBV;lr;r2=on;transport=tcp;ue>
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 521

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149132 0 IN IP4 10.46.0.20
s=-
c=IN IP4 10.46.0.20
b=AS:30
b=RS:375
b=RR:1125
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:375
b=RR:1125
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=inactive
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
a=conf:qos remote sendrecv
