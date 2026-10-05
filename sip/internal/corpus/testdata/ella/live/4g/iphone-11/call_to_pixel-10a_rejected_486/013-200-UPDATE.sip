SIP/2.0 200 OK
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=EOBHXAMMB2
To: <tel:+15550000002>;tag=99df5ff6
Contact: <sip:15550000002@10.46.0.24:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: qfbVCVt6QJ54MV64hnH5NzAf
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK5ARLGZQF4C5DEFG6ZVZ5C7HXSO,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKUWVHLESLCZJK2GNT6P7VHWLYOA,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK7KL5SGBMCFZEDQDCTFBVVGSPHW,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKTDJFB4DL6I5GH65DL67UKXNMVC,SIP/2.0/TCP 10.46.0.25:63507;received=10.46.0.25;branch=z9hG4bKFaqLi1fYmTkwbf3;rport=64105
Content-Length: 447
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010019b01
Server: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Require: precondition
Session-ID: 184f160cbf8bcce60fb030aaf370cb20

v=0
o=sip:IMS_RCS-USER 12345682 12345680 IN IP4 10.46.0.24
s=VOLTECALL
c=IN IP4 10.46.0.24
t=0 0
m=audio 7010 RTP/AVP 116 105
b=AS:30
b=RS:0
b=RR:0
a=rtpmap:116 EVS/16000/1
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
