INVITE sip:999010000000007@10.46.0.5:6100 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKXP2XY4J5HB4UJXR3M2M3ROAQAX
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKZCSXEAHSDOFJTGOCDET74G6TQ2
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKDEEWDR4JLVCJTXVPNK2YBAXTBI
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBHXM4ZSGPICJEBRPB55J7RN3F7
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKRRASEDSR6ZBJS4B4D5QTVQALEO
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=7022197
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>
CSeq: 7022189 INVITE
Call-ID: 7022189_3071011792@10.46.0.10
Via: SIP/2.0/TCP 10.46.0.10:44417;branch=z9hG4bK3550691292
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MIMAOHYYMLGW3HWO;ue>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=MIMAOHYYMLGW3HWO>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=PAJEAODRYRU4WCLV>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=WKFEAAXSOHIFOTX2>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=6MZTY7B6SYMJA37E>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=6MZTY7B6SYMJA37E;ue>
Max-Forwards: 65
Contact: <sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 764
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000007>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000008>

v=0
o=- 11 1000 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50012 RTP/AVP 104 110 102 108 96 97
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:110 AMR-WB/16000/1
a=fmtp:110 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:102 AMR/8000/1
a=fmtp:102 mode-change-capability=2;max-red=0
a=rtpmap:108 AMR/8000/1
a=fmtp:108 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
