UPDATE sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKOMFI26ACENUY7SVYOSVGHBKZRG
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKIVV2PBFXI7A6YJDUTK3CI2SQNA
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK3ZVQNLAFSQLHI5WAP4XX3YSHIH
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKM2V7MDXQQ6DG67XDMVR7FGN2YJ
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=d3af300a
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=102798606
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: 14d10c0fa0cb4772@10.46.0.3
CSeq: 3 UPDATE
Max-Forwards: 66
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKa2a899ac9c4e080ff34a799a
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=XRAF5LQK2ICZBR3O;ue>
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=XRAF5LQK2ICZBR3O>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=NWZPA7PWVDCOMWGI>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=TWUH23YUJAVZHAUB>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=NA2NUYJQ6NIKNRVJ>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=NA2NUYJQ6NIKNRVJ;ue>
Content-Length: 451
Content-Type: application/sdp
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Session-ID: 225ec9f5ff30530a90edeb2fc05d8e50
Require: precondition

v=0
o=sip:IMS_RCS-USER 12345678 12345679 IN IP4 10.46.0.3
s=VOLTECALL
c=IN IP4 10.46.0.3
t=0 0
m=audio 7010 RTP/AVP 100 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
