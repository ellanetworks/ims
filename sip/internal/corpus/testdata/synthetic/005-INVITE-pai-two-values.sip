INVITE sip:001010000000002@[2001:db8:10::6]:50000 SIP/2.0
Via: SIP/2.0/UDP [2001:db8::1]:5102;branch=z9hG4bKpcscf2
Via: SIP/2.0/UDP [2001:db8::2]:6060;branch=z9hG4bKscscf1;received=2001:db8::2
Via: SIP/2.0/TCP [2001:db8:10::5]:50001;branch=z9hG4bKinv6tel;rport=50001;received=2001:db8:10::5
Max-Forwards: 67
Record-Route: <sip:mt@[2001:db8::1]:5102;lr>, <sip:mt@[2001:db8::2]:6060;lr>
Record-Route: "S-CSCF" <sip:mo@[2001:db8::2]:6060;lr>;x-rr=1
Record-Route: <sip:mo@[2001:db8::1]:5103;transport=tcp;lr>
From: "Alice" <tel:+15551230001>;tag=f00d
To: <tel:+15551230002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Call-ID: inv6tel-0001@2001:db8:10::5
CSeq: 1 INVITE
Contact: <sip:001010000000001@[2001:db8:10::5]:50000>;+sip.instance="<urn:gsma:imei:00000000-000000-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
P-Asserted-Identity: "Alice" <sip:+15551230001@ims.mnc001.mcc001.3gppnetwork.org;user=phone>, <tel:+15551230001>
P-Called-Party-ID: <sip:+15551230002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
P-Charging-Vector: icid-value="1234bc9876e";icid-generated-at=[2001:db8::1];orig-ioi=ims.mnc001.mcc001.3gppnetwork.org
Privacy: none
Supported: 100rel,precondition,timer
Require: precondition
Session-Expires: 1800;refresher=uac
Content-Type: application/sdp
Content-Length: 505

v=0
o=- 1 1 IN IP6 2001:db8:10::5
s=-
c=IN IP6 2001:db8:10::5
b=AS:49
t=0 0
m=audio 50010 RTP/AVP 116 118 100
b=AS:49
b=RS:0
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-change-capability=2;max-red=0
a=rtpmap:100 telephone-event/16000
a=fmtp:100 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
