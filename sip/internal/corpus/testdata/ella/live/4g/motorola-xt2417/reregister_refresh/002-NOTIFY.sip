NOTIFY sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKWKDK75UL3D4WGRWYH3C4UCN57K
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKFCL3GNVERDYX6U5O5GYOVIIMPN
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NBKNYCFYHLNCTAR3OSCVDANUKJ
To: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=50235
Call-ID: 50232_4111137312@10.46.0.14
CSeq: 1133818749 NOTIFY
Content-Length: 1867
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=597001
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="1" state="full"><registration aor="sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org" id="r57c0916cf3867058" state="active"><contact id="c3ad074c60e02ef10" state="active" event="refreshed" expires="3600" callid="49935_4109409888@10.46.0.14" cseq="49937"><uri>sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000001-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration><registration aor="tel:+15550000001" id="rfdfda9c4841c24da" state="active"><contact id="cd6e848111839b240" state="active" event="refreshed" expires="3600" callid="49935_4109409888@10.46.0.14" cseq="49937"><uri>sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000001-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration></reginfo>