INVITE sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKMNA6NUKWNWOJLB4XBD6FA7LEIL
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK57H2I624GAKFA275PJ5PTDWUOV
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK5ZS7U3FSY3GLM5FCYI4DSO4LDC
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKIHRXOW5P3ZYVD6B4HLWPOAUDZL
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKRHF4KM3UQFMWYKDK5QNPMX2ABL
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=956055
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 956048 INVITE
Call-ID: 956048_3070404240@10.46.0.15
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK2770294180
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2K6RPNQ77TYFOPHJ;ue>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=2K6RPNQ77TYFOPHJ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=HTNEZOIG3W7NPTNH>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=GWICAY2QGKOEVK47>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=TLHZAYZAOS2BJJTQ>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=TLHZAYZAOS2BJJTQ;ue>
Max-Forwards: 65
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 764
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000001>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000004>

v=0
o=- 15 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50050 RTP/AVP 104 110 102 108 96 97
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
