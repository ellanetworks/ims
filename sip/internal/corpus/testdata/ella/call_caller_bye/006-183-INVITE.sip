SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.0.0.2:38873;branch=z9hG4bKFGRSOV5F7SLFZSS5NP2ISLCO25;rport=45067;received=10.0.0.2
Record-Route: <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5063;transport=tcp;lr;r2=on;did=AREWF3EVUTIQZAPC;ue>
Record-Route: <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5060;lr;r2=on;did=AREWF3EVUTIQZAPC>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=PZJ7LAPP47A5KB7T>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=4NIXFEQLROT7M6HP>
Record-Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5060;lr;r2=on;did=YRUSJYMQF73FRANU>
Record-Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5063;transport=tcp;lr;r2=on;did=YRUSJYMQF73FRANU;ue>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=B4GII4SE6BTWFZPJVJCHLBPG24
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=CSL6GIEWFVT6NIDLWWEAISEMIL
Call-ID: 4552e3a2-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 1 INVITE
Content-Length: 307
Contact: <sip:4542e7f2-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:35047>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Supported: 100rel, timer
Content-Type: application/sdp
Require: 100rel
RSeq: 317222256
P-Asserted-Identity: <tel:+15550002>
P-Asserted-Identity: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=- 117379067674626 0 IN IP4 10.0.0.3
s=-
c=IN IP4 10.0.0.3
t=0 0
m=audio 40000 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=sendrecv
a=ptime:20
a=maxptime:240
