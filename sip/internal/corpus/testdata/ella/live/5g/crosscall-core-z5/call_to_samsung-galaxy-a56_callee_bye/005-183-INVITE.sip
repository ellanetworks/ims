SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKCFMYQYRJXL5Q4QIR332C276FQY
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKWMP3AFPTNY7RCFMMCWWHOVR5GG
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKNQNZ3U22GWT4O3GVH5BANYKR7X
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKD2AG53KQAV5TQ7YHD43TA4NBFT
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKMQMNYDU5DBFMIRHQRBCEY2XDNR
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK731078867
Max-Forwards: 70
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=4NKCGDJOCMC2HPOJ;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=4NKCGDJOCMC2HPOJ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=OCQD6PWAEM3MXY4W>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=E6U2IYYL6IBKRX6G>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=O4LDYHSICKSZYIHD>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=O4LDYHSICKSZYIHD;ue>
Require: precondition, 100rel
Contact: <sip:15550000007@10.46.0.7:6600>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=c9048402
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100513383
Call-ID: 100513376_3069421008@10.46.0.8
CSeq: 100513376 INVITE
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
RSeq: 1
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066c000
Content-Length: 467

v=0
o=SAMSUNG-IMS-UE 186363504175 0 IN IP4 10.46.0.7
s=SS VOIP
c=IN IP4 10.46.0.7
t=0 0
m=audio 1250 RTP/AVP 104 96
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
