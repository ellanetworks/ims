BYE sip:001010000000002@[2001:db8:10::6]:50000 SIP/2.0
v: SIP/2.0/UDP [2001:db8::1]:5102;branch=z9hG4bKbye1
v: SIP/2.0/TCP [2001:db8:10::5]:50001;branch=z9hG4bKbye0;rport=50001;received=2001:db8:10::5
Max-Forwards: 69
Route: <sip:mt@[2001:db8::2]:6060;lr>,
 <sip:mt@[2001:db8::1]:5102;lr>
f: "Alice" <tel:+15551230001>;tag=f00d
t: <tel:+15551230002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=beef
i: inv6tel-0001@2001:db8:10::5
CSeq: 3 BYE
k: timer
P-Access-Network-Info: 3GPP-NR-FDD;
	utran-cell-id-3gpp=00101000001000000001
Reason: Q.850;cause=16;text="Normal call clearing"
l: 0

