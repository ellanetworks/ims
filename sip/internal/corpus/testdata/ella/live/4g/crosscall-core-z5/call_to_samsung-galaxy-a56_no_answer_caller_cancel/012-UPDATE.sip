UPDATE sip:15550000007@10.46.0.5:6100 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKX3G27JUXIPMBFMCMQFXKDXAKCH
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKTUZR6CCXRBFGPY74E7WNGB5YE3
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKHJJ5VY2VOKRJEI4G57V6VBPNH5
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKQWT4N55KA4LQ6AA6TAJCVF6VF4
Max-Forwards: 66
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=0542ad56
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=7022197
Call-ID: 7022189_3071011792@10.46.0.10
CSeq: 7022191 UPDATE
Content-Length: 456
Via: SIP/2.0/TCP 10.46.0.10:44417;branch=z9hG4bK1545228194
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MIMAOHYYMLGW3HWO;ue>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=MIMAOHYYMLGW3HWO>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=PAJEAODRYRU4WCLV>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=WKFEAAXSOHIFOTX2>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=6MZTY7B6SYMJA37E>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=6MZTY7B6SYMJA37E;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 11 1001 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50012 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
