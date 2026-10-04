INVITE tel:+15550000006 SIP/2.0
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Early-Media: supported
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=qFoDhZ8t3I
To: <tel:+15550000006>
Call-ID: CbGVybVmwugZ5NcE1udfVl2Z
Session-ID: 214e9273547593d0a7821e7caa4e266d
Contact: <sip:10.46.0.19:54286>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bKN90UrdxexGjQFd4;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
P-Preferred-Identity: sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
Require: sec-agree
Proxy-Require: sec-agree
Route: <sip:10.80.0.5:5063;lr>, <sip:orig-32@scscf.ims.mnc001.mcc001.3gppnetwork.org:5080;lr>
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=227428419;spi-s=59663401
Content-Type: application/sdp
Content-Length: 815

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791148937 1791148937 IN IP4 10.46.0.19
s=-
c=IN IP4 10.46.0.19
b=AS:42
b=RS:525
b=RR:1575
t=0 0
m=audio 49120 RTP/AVP 116 96 99 97 105 100
b=AS:42
b=RS:525
b=RR:1575
a=rtpmap:116 EVS/16000
a=fmtp:116 br=5.9-24.4; bw=nb-swb; max-red=220; mode-set=0,1,2
a=rtpmap:96 EVS/16000
a=fmtp:96 br=5.9-24.4; bw=nb-swb; max-red=220; mode-set=0,1,2
a=rtpmap:99 AMR-WB/16000
a=fmtp:99 max-red=0; mode-change-capability=2
a=rtpmap:97 AMR/8000
a=fmtp:97 max-red=0; mode-change-capability=2
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=rtpmap:100 telephone-event/8000
a=fmtp:100 0-15
a=ptime:20
a=maxptime:240
a=inactive
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos optional remote sendrecv
a=curr:qos remote none
