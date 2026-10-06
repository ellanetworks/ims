UPDATE sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQWHAKN5C62E2OAU62WYLQO4QDD
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKZ3EONIXHNSBDTRGGUBRWBNQSEF
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKO7DVAKFBZP34PCUA3P2E3NZR4I
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKLT3AASQBBNI7RDM4VMXCRPJU73
Via: SIP/2.0/TCP 10.46.0.7:6600;branch=z9hG4bK-524287-1---049c32b76d32c497;rport=6601;transport=TCP;received=10.46.0.7
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2AXOPYHCFZJCPDGI;ue>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=2AXOPYHCFZJCPDGI>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=LMFV3M434C4WGM5L>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=PRWHK5F2LTQVRP77>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=W4LQBCOD7YORI5O7>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=W4LQBCOD7YORI5O7;ue>
Max-Forwards: 66
Contact: <sip:15550000007@10.46.0.7:6600>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=100223009
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=14031060
Call-ID: o9voWSJ5T_Wr2ef6EsB2vg..@10.46.0.7
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Content-Length: 446
Require: precondition

v=0
o=SAMSUNG-IMS-UE 186072712835 1 IN IP4 10.46.0.7
s=SS VOIP
c=IN IP4 10.46.0.7
t=0 0
m=audio 1246 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=ptime:20
a=maxptime:240
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
