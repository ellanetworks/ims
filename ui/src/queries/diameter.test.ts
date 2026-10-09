import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createDiameterPeer,
  deleteDiameterPeer,
  listDiameterPeers,
  listDiameterRoutes,
  updateDiameterPeer,
  updateDiameterRoute,
  type DiameterPeerParams,
} from "@/queries/diameter";

const stubFetch = (result: unknown) => {
  const fetchMock = vi.fn(
    async (_url: string, _init?: RequestInit) =>
      new Response(JSON.stringify({ result }), { status: 200 }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

const params: DiameterPeerParams = {
  host: "hss.example.org",
  address: "10.0.0.1",
  applications: ["cx"],
  priority: 1,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listDiameterPeers", () => {
  it("returns the peers", async () => {
    const peers = [{ id: "a" }, { id: "b" }];
    stubFetch({ items: peers });

    await expect(listDiameterPeers()).resolves.toEqual(peers);
  });
});

describe("createDiameterPeer", () => {
  it("posts the peer", async () => {
    const fetchMock = stubFetch({ id: "a", ...params });

    await createDiameterPeer(params);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/diameter/peers");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: "POST",
      body: JSON.stringify(params),
    });
  });
});

describe("updateDiameterPeer", () => {
  it("puts the peer at its id", async () => {
    const fetchMock = stubFetch({ id: "a", ...params });

    await updateDiameterPeer("a", params);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/diameter/peers/a");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: "PUT",
      body: JSON.stringify(params),
    });
  });
});

describe("deleteDiameterPeer", () => {
  it("deletes the peer at its id", async () => {
    const fetchMock = stubFetch({ message: "Diameter peer deleted" });

    await expect(deleteDiameterPeer("a")).resolves.toBeUndefined();

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/diameter/peers/a");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "DELETE" });
  });
});

describe("listDiameterRoutes", () => {
  it("returns the routes", async () => {
    const routes = [{ application: "cx" }, { application: "rx" }];
    const fetchMock = stubFetch({ items: routes });

    await expect(listDiameterRoutes()).resolves.toEqual(routes);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/diameter/routes");
  });
});

describe("updateDiameterRoute", () => {
  it("puts the realm at the application", async () => {
    const fetchMock = stubFetch({ application: "rx", realm: "example.org" });

    await updateDiameterRoute("rx", "example.org");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/diameter/routes/rx");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: "PUT",
      body: JSON.stringify({ realm: "example.org" }),
    });
  });
});
