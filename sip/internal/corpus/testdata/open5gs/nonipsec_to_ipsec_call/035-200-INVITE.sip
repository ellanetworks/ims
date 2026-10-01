SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.4.128.21:5101;branch=z9hG4bK871a.a5cd3a8d2d34670b173e76a6bab6d24e.0;transport=TCP
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK871a.bbc9d5d400ee2e0a9b36c4f10640d958.0
Via: SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bK871a.ee737663fa704bc202c40661664fb603.1
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK871a.79c06bcb4044312db43553666beb849f.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK871a.2e2ff827f034d8034b71c49d100eba8c.0;i=3
Via: SIP/2.0/TCP 192.168.101.2:5060;rport=43551;branch=z9hG4bK1185684359
Max-Forwards: 70
Record-Route: <sip:mt@10.4.128.21:5101;r2=on;lr=on;ftag=4128004116;rm=7;did=a22.d192>
Record-Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=4128004116;rm=7;did=a22.d192>
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=4128004116;did=a22.9692>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=4128004116;did=a22.8692>
Record-Route: <sip:mo@10.4.128.21;r2=on;lr=on;ftag=4128004116;rm=8;did=a22.c192>
Record-Route: <sip:mo@10.4.128.21;transport=tcp;r2=on;lr=on;ftag=4128004116;rm=8;did=a22.c192>
Require: timer
Contact: <sip:0398765432100@192.168.101.4:6200>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:0398765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=622eae5e
From: <tel:0598765432100>;tag=4128004116
Call-ID: 4128004109_45009256@192.168.101.2
CSeq: 906778637 INVITE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: timer, 100rel
User-Agent: SM-T515-TB4 Samsung IMS 6.0
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 0

