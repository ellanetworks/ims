INVITE tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103310767
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 103310760 INVITE
Call-ID: 103310760_1760534736@10.46.0.2
Via: SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK1922361788
Max-Forwards: 70
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Route: <sip:10.80.0.5:5063;lr>,<sip:orig-6@scscf.ims.mnc001.mcc001.3gppnetwork.org:5080;lr>
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=191483176;spi-s=53304908;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Proxy-Require: sec-agree
Require: sec-agree
P-Preferred-Identity: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
P-Early-Media: supported
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 762
Session-Expires: 1800;refresher=uac

v=0
o=- 13 1000 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50024 RTP/AVP 104 110 102 108 96 97
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
