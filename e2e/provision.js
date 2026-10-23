const K = "465b5ce8b199b49faa5f0a2ee238a6bc";
const OPC = "cd63cb71954a9f4e48a5994e37a02baf";

// The subscribers of the UEs (compose.4g.yaml, compose.5g.yaml): alice, bob and carol of mcptt/provision.sh.
const subscribers = [
  { imsi: "001010000000001", msisdn: "15550000001" },
  { imsi: "001010000000002", msisdn: "15550000002" },
  { imsi: "001010000000003", msisdn: "15550000003" },
];

// The MCPTT AS of Ella IMS, its name of the home domain, which the IMS resolves to it in memory.
const mcpttServer = "sip:mcptt.ims.mnc001.mcc001.3gppnetwork.org";

// The iFCs of the MCPTT server (TS 29.228 Annex B.2.2; the fields of Open5GS, lib/dbi/ims.c and open5gs/ifc.patch),
// which the originating S-CSCF evaluates (TS 24.229 §5.4.1.7, §5.4.3.2 step 5):
// - every REGISTER of the user, of any registration type, gets a third-party REGISTER with the UE's REGISTER in its
//   body, from which the server takes the mcptt-info of the service authorisation (TS 24.379 §7.3.2, TS 24.229
//   §5.4.1.7 g, §5.4.1.7A), and the 200 to it (for its P-Associated-URI and the GRUUs); the registration goes on
//   without the server (SESSION_CONTINUED);
// - the initial requests the user originates for MCPTT, whose P-Asserted-Service the S-CSCF sets from their
//   P-Preferred-Service (TS 24.379 §10.1.1.2.1.1 step 6, §9.2.1.2 step 3; TS 24.229 §5.4.3.2 steps 4C, 4D), go to
//   the participating function, their Request-URI its PSI (TS 24.379 §10.1.1.2.1.1 step 10); without the server,
//   they fail (SESSION_TERMINATED): no other node serves them.
// The requests of the server to the user are terminating, past both. The expressions are regular expressions of a
// header value (TS 29.228 Annex F).
//
// SESSION_CONTINUED is the default of the REGISTER iFC: the IMS registration of a UE goes on without MCPTT service
// when the server does not answer. For a dedicated MC UE, which has no service without MCPTT, SESSION_TERMINATED
// (default_handling 1) is the option: the S-CSCF then deregisters the UE (TS 24.229 §5.4.1.7, the paragraph after
// item k), which registers again and so retries its service authorisation, at the cost of all its other IMS services.
//
// What another core needs for these iFCs:
// - an S-CSCF that sets P-Asserted-Service from P-Preferred-Service (TS 24.229 §5.4.3.2 step 4C), which the
//   originating iFC triggers on, headers "created by S-CSCF" being trigger material (TS 23.218 §6.9.2.3): the
//   Kamailio S-CSCF, for one, sets none; and the MCPTT ICSI among the subscribed services of the user, as the S-CSCF
//   may otherwise drop the P-Preferred-Service (step 4C a);
// - in a service profile that also has the iFC of an MMTel TAS, often an unconditioned originating INVITE, the MCPTT
//   iFCs of a lower priority value, evaluated first (TS 29.228 Annex B.2.2: Priority), or the MMTel iFC conditioned
//   on the MMTel ICSI.
const ifc = [
  {
    priority: NumberInt(0),
    trigger_point: {
      condition_type_cnf: NumberInt(0),
      spt: [{ condition_negated: NumberInt(0), group: NumberInt(0), method: "REGISTER" }],
    },
    application_server: {
      server_name: mcpttServer,
      default_handling: NumberInt(0),
      include_register_request: true,
      include_register_response: true,
    },
  },
  {
    priority: NumberInt(1),
    trigger_point: {
      condition_type_cnf: NumberInt(0),
      spt: [
        { condition_negated: NumberInt(0), group: NumberInt(0), session_case: NumberInt(0) },
        {
          condition_negated: NumberInt(0),
          group: NumberInt(0),
          sip_header: { header: "^P-Asserted-Service$", content: "^urn:urn-7:3gpp-service\\.ims\\.icsi\\.mcptt$" },
        },
      ],
    },
    application_server: { server_name: mcpttServer, default_handling: NumberInt(1) },
  },
];

const bitrate = (v) => ({ value: NumberInt(v), unit: NumberInt(1) });

const arp = (level, capability, vulnerability) => ({
  priority_level: NumberInt(level),
  pre_emption_capability: NumberInt(capability),
  pre_emption_vulnerability: NumberInt(vulnerability),
});

// The PCC rules a media component of an IMS call takes the QCI or 5QI, the ARP and, when the P-CSCF requests none,
// the bitrates of (open5gs/qos.patch): 65 the voice and 69 the floor control channel of an MCPTT call (TS 23.379
// §5.7.3), the P-CSCF giving the MCPTT ICSI as AF application identifier (TS 29.214 Annex A.13.1 NOTE 3, TS 29.514
// Annex B.10.1 NOTE 3); 1 the audio and 2 the application media of others (TS 29.213 Table 6.3.1, TS 29.513
// Table 7.3.3-1). 69 is non-GBR (TS 23.501 Table 5.7.4-1): no bitrates. An application media component without a
// rule of its QCI stays on the default bearer: in 4G, which has none, as the srsRAN eNB and UE of compose.4g.yaml lose
// the radio link (RLC max retransmissions on the default bearer) when a call sets up two dedicated bearers of a UE.
// rat is set by up.sh.
const pccRule = (qci, level, gbr = true) => ({
  qos: {
    index: NumberInt(qci),
    arp: arp(level, 2, 2),
    ...(gbr && {
      mbr: { downlink: bitrate(128), uplink: bitrate(128) },
      gbr: { downlink: bitrate(128), uplink: bitrate(128) },
    }),
  },
  flow: [],
});

// One line: mongosh reads its standard input as a REPL, a statement per line.
const pccRules = rat === "4g" ? [pccRule(1, 2), pccRule(65, 2)] : [pccRule(1, 2), pccRule(2, 4), pccRule(65, 2), pccRule(69, 2, false)];

db = db.getSiblingDB("open5gs");

for (const s of subscribers) {
  db.subscribers.replaceOne(
    { imsi: s.imsi },
    {
      schema_version: NumberInt(1),
      imsi: s.imsi,
      msisdn: [s.msisdn],
      imeisv: [],
      mme_host: [],
      mm_realm: [],
      purge_flag: [],
      slice: [{
        sst: NumberInt(1),
        default_indicator: true,
        session: [{
          name: "ims",
          type: NumberInt(1),
          qos: { index: NumberInt(5), arp: arp(1, 1, 1) },
          ambr: { downlink: bitrate(3850), uplink: bitrate(1530) },
          pcc_rule: pccRules,
        }],
      }],
      security: { k: K, op: null, opc: OPC, amf: "8000", sqn: NumberLong(0) },
      ambr: { downlink: bitrate(1000000), uplink: bitrate(1000000) },
      access_restriction_data: NumberInt(32),
      network_access_mode: NumberInt(0),
      subscriber_status: NumberInt(0),
      operator_determined_barring: NumberInt(0),
      subscribed_rau_tau_timer: NumberInt(12),
      ifc: ifc,
      __v: NumberInt(0),
    },
    { upsert: true },
  );
}

printjson(db.subscribers.find({}, { imsi: 1, msisdn: 1, ifc: 1, _id: 0 }).toArray());
