INVITE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKEHZYCA2FRE5U3KFONPMP2SQL3P
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5JNXZQRFKIJTMY5CO7S3O2XFVX
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK3Q7KXXFXTD7YC5ZTJ6HRVHG7DX
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQ54Q3YCBFBNSWSZ2YUEX454VNA
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKN4R355KK2D3DA3I65CWVNCA2BD
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=394072
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 394065 INVITE
Call-ID: 394065_1760685952@10.46.0.13
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK2233954828
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=7GG5H3RMN2RF7OIZ;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=7GG5H3RMN2RF7OIZ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=7X6ORYJQHEJWZ5MV>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=OWJA5MIMJ24SYSKN>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=GG3KIJ6ALIP7ALU3>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=GG3KIJ6ALIP7ALU3;ue>
Max-Forwards: 65
Contact: <sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 763
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000004>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000005>

v=0
o=- 7 1000 IN IP4 10.46.0.13
s=QC VOIP
c=IN IP4 10.46.0.13
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50048 RTP/AVP 104 110 102 108 96 97
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
