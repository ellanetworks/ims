import type { DiameterPeerParams, DiameterTransport } from "@/queries/diameter";

export const RESTART_WARNING =
  "Diameter and SIP restart: phones re-register, and calls in progress lose their QoS.";

type Peer = DiameterPeerParams & { id?: string };

const transports = (peers: Peer[]): Set<DiameterTransport> =>
  new Set(peers.map((p) => p.transport ?? "tcp"));

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
