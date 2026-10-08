import type { DiameterPeer } from "@/queries/diameter";
import type { Operator } from "@/queries/operator";
import type { PolicyWithStatus } from "@/queries/policy";
import type { SIPStatus } from "@/queries/sip";

export const operator: Operator = {
  mcc: "001",
  mnc: "01",
  numbering: {
    country_code: "1",
    national_prefix: "",
    international_prefix: "",
  },
};

export const identity = {
  host: "ims.ims.mnc001.mcc001.3gppnetwork.org",
  realm: "ims.mnc001.mcc001.3gppnetwork.org",
};

export const sip: SIPStatus = {
  home_domain: "ims.mnc001.mcc001.3gppnetwork.org",
  aliases: [],
  listeners: [
    { role: "pcscf", address: "192.0.2.20:5060", transports: ["udp", "tcp"] },
    { role: "pcscf", address: "[2001:db8::20]:5060", transports: ["udp"] },
    { role: "pcscf-protected", address: "192.0.2.20:5063", transports: [] },
    { role: "icscf", address: "192.0.2.20:5070", transports: ["udp"] },
  ],
};

export const peer = (overrides: Partial<DiameterPeer> = {}): DiameterPeer => ({
  id: "0199a1b2-0000-7000-8000-000000000001",
  host: "core.epc.mnc001.mcc001.3gppnetwork.org",
  realm: "epc.mnc001.mcc001.3gppnetwork.org",
  address: "192.0.2.1",
  port: 3868,
  transport: "sctp",
  applications: ["cx", "rx"],
  status: { state: "open", since: "2026-10-08T12:00:00.000Z" },
  ...overrides,
});

export const policy = (
  overrides: Partial<PolicyWithStatus> = {},
): PolicyWithStatus => ({
  interface: "none",
  status: { interface: "none" },
  ...overrides,
});
