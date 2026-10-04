UPDATE sip:10.46.0.17:61189 SIP/2.0
Require: precondition,sec-agree
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000006>;tag=hCj5citYZC
Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;did=SQDVSKQGRKXLGWGR;lr;r2=on;ue>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;did=SQDVSKQGRKXLGWGR;lr;r2=on>, <sip:mo@10.80.0.5:5080;did=V4AD5R6ITUR3MFX6;lr>, <sip:mt@10.80.0.5:5080;did=MYUSMYNP223LEG6B;lr>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=F5UCRKKYIXMYNXA3;lr;r2=on>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=F5UCRKKYIXMYNXA3;lr;r2=on;transport=tcp;ue>
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=qFoDhZ8t3I
Call-ID: CbGVybVmwugZ5NcE1udfVl2Z
Session-ID: 214e9273547593d0a7821e7caa4e266d
Contact: <sip:10.46.0.19:54286>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bKLijqWmzN2NoUX0E;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=227428419;spi-s=59663401
Proxy-Require: sec-agree
Content-Type: application/sdp
Content-Length: 510

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791148937 1791148938 IN IP4 10.46.0.19
s=-
c=IN IP4 10.46.0.19
b=AS:42
b=RS:525
b=RR:1575
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:42
b=RS:525
b=RR:1575
a=rtpmap:116 EVS/16000
a=fmtp:116 br=5.9-24.4; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
