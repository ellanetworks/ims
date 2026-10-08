import { afterEach, describe, expect, it, vi } from "vitest";
import { reauthenticate } from "@/queries/registrations";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("reauthenticate", () => {
  it("posts to the encoded IMPI", async () => {
    const impi = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org";
    const fetchMock = vi.fn(
      async (_url: string, _init?: RequestInit) =>
        new Response(JSON.stringify({ result: { impi } }), { status: 202 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(reauthenticate(impi)).resolves.toEqual({ impi });

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/v1/registrations/001010000000001%40ims.mnc001.mcc001.3gppnetwork.org/reauthenticate",
    );
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST" });
  });
});
