NOTIFY sip:192.168.101.2:5060;alias=192.168.101.2~5060~1 SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bKdcb4.f867fa91000000000000000000000000.0
To: <tel:0598765432100>;tag=4130282331
From: <tel:0598765432100>;tag=a35a5806d6040414c4d26ea88c1e71a0-5cb90000
CSeq: 909056859 NOTIFY
Call-ID: 4130282328_46852536@192.168.101.2
Route: <sip:mo@10.4.128.21;lr=on;ftag=4130282331>
Content-Length: 1229
User-Agent: Kamailio S-CSCF
Contact: <sip:scscf.ims.mnc001.mcc001.3gppnetwork.org:6060>
Event: reg
Max-Forwards: 70
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="2" state="full">
	<registration aor="tel:0598765432100" id="0x7f45bcb229f8" state="active">
		<contact id="0x7f45bcb21ca8" state="active" event="registered" expires="600000" q="0.000">
			<uri>sip:192.168.101.2:5060;alias=192.168.101.2~5060~1</uri>
		<unknown-param name="+sip.instance">"&lt;urn:gsma:imei:86728703-952237-0&gt;"</unknown-param>
		<unknown-param name="video"></unknown-param>
		<unknown-param name="+g.3gpp.smsip"></unknown-param>
		<unknown-param name="+g.3gpp.icsi-ref">"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"</unknown-param>
		</contact>
	</registration>
	<registration aor="sip:0598765432100" id="0x7f45bcb22f68" state="active">
		<contact id="0x7f45bcb21ca8" state="active" event="registered" expires="600000" q="0.000">
			<uri>sip:192.168.101.2:5060;alias=192.168.101.2~5060~1</uri>
		<unknown-param name="+sip.instance">"&lt;urn:gsma:imei:86728703-952237-0&gt;"</unknown-param>
		<unknown-param name="video"></unknown-param>
		<unknown-param name="+g.3gpp.smsip"></unknown-param>
		<unknown-param name="+g.3gpp.icsi-ref">"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"</unknown-param>
		</contact>
	</registration>
</reginfo>
