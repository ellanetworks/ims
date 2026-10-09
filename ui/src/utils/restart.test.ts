import { describe, expect, it } from "vitest";
import type { DiameterPeerParams } from "@/queries/diameter";
import { changesTransports } from "@/utils/restart";

type Peer = DiameterPeerParams & { id?: string };

const peer = (id: string, transport?: "tcp" | "sctp"): Peer => ({
  id,
  host: `${id}.example.org`,
  address: "192.0.2.1",
  applications: ["cx"],
  transport,
});

// The server restarts on the same changes (settings.SameNode).
describe("changesTransports", () => {
  it("is false for a TCP peer added beside a TCP one", () => {
    expect(changesTransports([peer("a")], undefined, peer("b", "tcp"))).toBe(
      false,
    );
  });

  it("is true for the first SCTP peer", () => {
    expect(changesTransports([peer("a")], undefined, peer("b", "sctp"))).toBe(
      true,
    );
  });

  it("is true for the only SCTP peer moved to TCP", () => {
    const sctp = peer("b", "sctp");
    expect(
      changesTransports([peer("a"), sctp], sctp, { ...sctp, transport: "tcp" }),
    ).toBe(true);
  });

  it("is false for a peer edited on the same transport", () => {
    const a = peer("a");
    expect(changesTransports([a], a, { ...a, priority: 1 })).toBe(false);
  });

  it("is true for the last peer deleted", () => {
    const a = peer("a");
    expect(changesTransports([a], a, undefined)).toBe(true);
  });

  it("is false for a peer deleted beside another of its transport", () => {
    const a = peer("a");
    expect(changesTransports([a, peer("b")], a, undefined)).toBe(false);
  });
});
