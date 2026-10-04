UPDATE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Require: precondition,sec-agree
Supported: 100rel,path,replaces,timer
To: <tel:+15550000004>;tag=2029281
Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=VMJQVCKFZ2ERSM43;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=VMJQVCKFZ2ERSM43;lr;r2=on>, <sip:mo@10.80.0.5:5080;did=IVINAR5TEFPZ4SSM;lr>, <sip:mt@10.80.0.5:5080;did=4R65NXDM3N6HEDHH;lr>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=IM3Z7F4LVJAYGX6Z;lr;r2=on>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=IM3Z7F4LVJAYGX6Z;lr;r2=on;transport=tcp;ue>
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=phGRFFh1Oz
Call-ID: XLSDSgMpObRgVituc0pOtPU2
Session-ID: b9ef98e3980320464cdc5f1eb9bf4d05
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKtBLMSw3aPRLT1Q2;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=189860270;spi-s=88237557
Proxy-Require: sec-agree
Content-Type: application/sdp
Content-Length: 501

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791147588 1791147589 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:41
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 99 105
b=AS:41
b=RS:0
b=RR:0
a=rtpmap:99 AMR-WB/16000
a=fmtp:99 max-red=0; mode-change-capability=2
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
