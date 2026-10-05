UPDATE sip:15550000002@10.46.0.24:49165 SIP/2.0
Require: precondition,sec-agree
Supported: 100rel,path,replaces,timer
To: <tel:+15550000002>;tag=99df5ff6
Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;did=CPNLDKUQZRYHEPCG;lr;r2=on;transport=tcp;ue>, <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;did=CPNLDKUQZRYHEPCG;lr;r2=on>, <sip:mo@10.80.0.5:5080;did=A6WQHUKUTBVHHHJN;lr>, <sip:mt@10.80.0.5:5080;did=2F5U5JWSV44XQ5KE;lr>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;did=QVL5T7W4UAKQEF5C;lr;ob;r2=on>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;did=QVL5T7W4UAKQEF5C;lr;ob;r2=on;transport=tcp;ue>
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=EOBHXAMMB2
Call-ID: qfbVCVt6QJ54MV64hnH5NzAf
Session-ID: 184f160cbf8bcce60fb030aaf370cb20
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.25:63507;branch=z9hG4bKFaqLi1fYmTkwbf3;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=8430301;spi-s=114600604
Proxy-Require: sec-agree
Content-Type: application/sdp
Content-Length: 496

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791202975 1791202976 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
b=AS:30
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:0
b=RR:0
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
