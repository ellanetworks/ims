import type { DiameterPeerParams, DiameterTransport } from "@/queries/diameter";

export const RESTART_WARNING =
  "Diameter and SIP restart: phones re-register, and calls in progress lose their QoS.";

type Peer = DiameterPeerParams & { id?: string };

const transports = (peers: Peer[]): Set<DiameterTransport> =>
  new Set(peers.map((p) => p.transport ?? "tcp"));

// changesTransports reports whether replacing a peer (before) with another (after), either one absent for an
// addition or a deletion, changes the transports the IMS listens on: the only peer change that restarts Diameter
// and SIP.
export const changesTransports = (
  peers: Peer[],
  before: Peer | undefined,
  after: Peer | undefined,
) => {
  const now = transports(peers);
  const next = transports([
    ...peers.filter((p) => before === undefined || p.id !== before.id),
    ...(after ? [after] : []),
  ]);
  return now.size !== next.size || [...now].some((t) => !next.has(t));
};
