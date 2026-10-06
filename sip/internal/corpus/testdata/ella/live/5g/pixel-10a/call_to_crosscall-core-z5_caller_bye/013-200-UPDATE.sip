SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKOMFI26ACENUY7SVYOSVGHBKZRG,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKIVV2PBFXI7A6YJDUTK3CI2SQNA,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK3ZVQNLAFSQLHI5WAP4XX3YSHIH,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKM2V7MDXQQ6DG67XDMVR7FGN2YJ,SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKa2a899ac9c4e080ff34a799a
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=d3af300a
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=102798606
Call-ID: 14d10c0fa0cb4772@10.46.0.3
CSeq: 3 UPDATE
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=XRAF5LQK2ICZBR3O;ue>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=XRAF5LQK2ICZBR3O>,<sip:mt@10.80.0.5:5080;lr;did=NWZPA7PWVDCOMWGI>,<sip:mo@10.80.0.5:5080;lr;did=TWUH23YUJAVZHAUB>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=NA2NUYJQ6NIKNRVJ>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=NA2NUYJQ6NIKNRVJ;ue>
Content-Length: 457
Content-Type: application/sdp
Session-ID: 225ec9f5ff30530a90edeb2fc05d8e50
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uac

v=0
o=- 8 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50046 RTP/AVP 100 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
