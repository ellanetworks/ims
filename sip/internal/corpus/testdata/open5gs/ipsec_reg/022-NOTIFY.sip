NOTIFY sip:0398765432100@192.168.101.5:6300;alias=192.168.101.5~6301~1 SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21:5100;branch=z9hG4bK7b2f.6d0f79b2cc6a284fcbd6c6d474f9fa18.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK7b2f.dd0cf4e1000000000000000000000000.0
To: <tel:0398765432100>;tag=6197e96a
From: <tel:0398765432100>;tag=a35a5806d6040414c4d26ea88c1e71a0-4b556d32
CSeq: 4 NOTIFY
Call-ID: yQFjFiHYzbfiJeASucHAGQ..@192.168.101.5
Content-Length: 1227
User-Agent: Kamailio S-CSCF
Contact: <sip:scscf.ims.mnc001.mcc001.3gppnetwork.org:6060>
Event: reg
Max-Forwards: 69
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-md5-96;ealg=null

<?xml version="1.0"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="2" state="full">
	<registration aor="tel:0398765432100" id="0x7f45bcb27c98" state="active">
		<contact id="0x7f45bcb27058" state="active" event="registered" expires="3599" q="1.000">
			<uri>sip:192.168.101.5:6300;alias=192.168.101.5~6301~2</uri>
		<unknown-param name="+g.3gpp.smsip"></unknown-param>
		<unknown-param name="+g.3gpp.icsi-ref">"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"</unknown-param>
		<unknown-param name="q">"1.0"</unknown-param>
		<unknown-param name="+sip.instance">"&lt;urn:gsma:imei:35622410-483840-0&gt;"</unknown-param>
		</contact>
	</registration>
	<registration aor="sip:0398765432100" id="0x7f45bcb28208" state="active">
		<contact id="0x7f45bcb27058" state="active" event="registered" expires="3599" q="1.000">
			<uri>sip:192.168.101.5:6300;alias=192.168.101.5~6301~2</uri>
		<unknown-param name="+g.3gpp.smsip"></unknown-param>
		<unknown-param name="+g.3gpp.icsi-ref">"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"</unknown-param>
		<unknown-param name="q">"1.0"</unknown-param>
		<unknown-param name="+sip.instance">"&lt;urn:gsma:imei:35622410-483840-0&gt;"</unknown-param>
		</contact>
	</registration>
</reginfo>
