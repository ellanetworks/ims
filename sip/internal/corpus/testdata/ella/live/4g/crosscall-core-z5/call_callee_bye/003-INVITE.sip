INVITE sip:6f0ab00c-bded-407e-a8a3-721fc172104b@10.46.0.11:41290 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKRVJYRQT2DZGFY7Z43J4T3Z63SN
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQRAZAHTL5OPKKEDQAADLF3E5LP
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKBKRPLIZ2SCXAGC6IRYE4ZPXYOW
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK67ZEYWCMW3K3F3HVNRWT3H3XSZ
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKNNVMDBAIULIUE3AXBFJ7WVMQJN
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=10999539
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 10999534 INVITE
Call-ID: 10999534_3068109552@10.46.0.12
Via: SIP/2.0/TCP 10.46.0.12:40079;branch=z9hG4bK2279200514
Record-Route: <sip:vi2bwmwnrzaymflx5aiknm7xty@10.80.0.5:5063;transport=tcp;lr;r2=on;did=PS2WY5JAQIREWCJM;ue>
Record-Route: <sip:vi2bwmwnrzaymflx5aiknm7xty@10.80.0.5:5060;lr;r2=on;did=PS2WY5JAQIREWCJM>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=2OXHFD2XSKML5XQY>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=E7Z55NQRC655NCXL>
Record-Route: <sip:w35g4hq4urdemhjo35tydi7tnf@10.80.0.5:5060;lr;r2=on;did=CMK44DQVMHKNCUTP>
Record-Route: <sip:w35g4hq4urdemhjo35tydi7tnf@10.80.0.5:5063;transport=tcp;lr;r2=on;did=CMK44DQVMHKNCUTP;ue>
Max-Forwards: 65
Contact: <sip:e17adb96-9fc3-4cf0-a7aa-db9e240fc2f3@10.46.0.12:40079>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 763
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000005>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000004>

v=0
o=- 6 1000 IN IP4 10.46.0.12
s=QC VOIP
c=IN IP4 10.46.0.12
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50060 RTP/AVP 104 110 102 108 96 97
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
