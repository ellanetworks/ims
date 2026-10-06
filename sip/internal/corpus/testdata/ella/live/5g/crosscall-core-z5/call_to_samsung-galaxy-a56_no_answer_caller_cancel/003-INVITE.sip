INVITE sip:999010000000007@10.46.0.7:6600 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKBNUM7UHYBYFID37OOR4YWB3JLV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKWS5YHAGP65CMU3LO2OLE5E7H5A
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKBL3JVSUQCQPDGMXCI6TJ62CESG
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKTFDNNO3PBWICCJUKFHFVC7ESAC
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKIOVCT33LVAJKYZHBNRIW2YRWTQ
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100791154
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>
CSeq: 100791147 INVITE
Call-ID: 100791148_3068993808@10.46.0.8
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK1853593200
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=M5SM2HEFRGAHI36A;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=M5SM2HEFRGAHI36A>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=Z4QDMBAGCXEEIS3F>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=WXYOJWTZ4LBBKHVN>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=752RRKDPKX56IKFN>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=752RRKDPKX56IKFN;ue>
Max-Forwards: 65
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 761
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000007>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000008>

v=0
o=- 9 1000 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50040 RTP/AVP 104 110 102 108 96 97
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
