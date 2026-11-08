import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import SMS from "@/pages/SMS";
import type { DiameterPeer, DiameterRoute } from "@/queries/diameter";
import type { SMSRoute } from "@/queries/ipsmgw";
import { identity, peer, route as routeOf } from "@/test/fixtures";
import { json, readOnlyAuth, renderWithClient, stubApi } from "@/test/render";

interface Request {
  method: string;
  path: string;
  body?: unknown;
}

const smsc = peer({
  id: "0199a1b2-0000-7000-8000-000000000002",
  host: "smsc.epc.mnc001.mcc001.3gppnetwork.org",
  address: "192.0.2.7",
  applications: ["sgd"],
  priority: 20,
});

const backup = peer({
  id: "0199a1b2-0000-7000-8000-000000000003",
  host: "backup.epc.mnc001.mcc001.3gppnetwork.org",
  address: "192.0.2.8",
  applications: ["sgd"],
  priority: 10,
  status: { state: "down", error: "connection refused" },
});

const defaultRoute: SMSRoute = {
  id: "0199a1b2-0000-7000-8000-000000000011",
  service_centre: "",
  peers: [smsc.id, backup.id],
};

const scRoute: SMSRoute = {
  id: "0199a1b2-0000-7000-8000-000000000012",
  service_centre: "15550000000",
  peers: [smsc.id],
};

const serve = ({
  peers = [peer(), smsc, backup],
  routes = [scRoute, defaultRoute],
  diameterRoutes = [routeOf(), routeOf({ application: "s6c" })],
  number = "15550000100",
}: {
  peers?: DiameterPeer[];
  routes?: SMSRoute[];
  diameterRoutes?: DiameterRoute[];
  number?: string;
} = {}) => {
  const requests: Request[] = [];
  const route =
    (answer: (req: Request) => Response) => (url: URL, init?: RequestInit) => {
      const req: Request = {
        method: init?.method ?? "GET",
        path: url.pathname,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      if (req.method !== "GET") requests.push(req);
      return answer(req);
    };

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/diameter": () => json(200, { result: identity }),
        "/api/v1/diameter/peers": () => json(200, { result: { items: peers } }),
        "/api/v1/diameter/routes": () =>
          json(200, { result: { items: diameterRoutes } }),
        "/api/v1/ip-sm-gw": route((req) =>
          json(200, {
            result: {
              number,
              ...(req.body as object),
              name: "sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org",
            },
          }),
        ),
        "/api/v1/ip-sm-gw/routes": route((req) =>
          req.method === "POST"
            ? json(201, { result: { id: "new", ...(req.body as object) } })
            : json(200, { result: { items: routes } }),
        ),
        [`/api/v1/ip-sm-gw/routes/${scRoute.id}`]: route((req) =>
          req.method === "DELETE"
            ? json(200, { result: { message: "SMS route deleted" } })
            : json(200, { result: { ...scRoute, ...(req.body as object) } }),
        ),
      }),
    ),
  );
  return requests;
};

const fill = (label: RegExp | string, value: string) =>
  fireEvent.change(screen.getByRole("textbox", { name: label }), {
    target: { value },
  });

const settingRows = (name: string) =>
  Array.from(
    screen.getByRole("table", { name }).querySelectorAll(":scope > tbody > tr"),
  ).map((row) => [
    row.querySelector("td")?.textContent,
    row.querySelector("td")?.nextElementSibling?.textContent,
  ]);

const routeRow = async (name: string) =>
  (await screen.findByText(name)).closest('[role="row"]') as HTMLElement;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SMS", () => {
  it("shows what to set in the HSS and SMSC", async () => {
    serve();

    renderWithClient(<SMS />);

    await screen.findByText(identity.host);
    expect(settingRows("IP-SM-GW")).toEqual([
      ["Name", "sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org"],
      ["Number", "15550000100"],
      ["Diameter Host", identity.host],
      ["Diameter Realm", identity.realm],
    ]);
    await waitFor(() =>
      expect(settingRows("HSS")).toEqual([
        ["S6c Realm", "ims.mnc001.mcc001.3gppnetwork.orgHSS realm"],
      ]),
    );
  });

  it("sets the IP-SM-GW number", async () => {
    const requests = serve({ number: "" });

    renderWithClient(<SMS />);

    expect(
      await screen.findByText("No IP-SM-GW number: phones get no SMS over IP."),
    ).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Edit Number" }));
    fill(/^Number/, "+1555");
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fill(/^Number/, "15550000100");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "PUT",
        path: "/api/v1/ip-sm-gw",
        body: { number: "15550000100" },
      },
    ]);
  });

  it("lists the routes, the default last, their peers by priority", async () => {
    serve();

    renderWithClient(<SMS />);

    const def = await routeRow("Default");
    expect(def).toHaveTextContent(
      "backup.epc.mnc001.mcc001.3gppnetwork.orgdownsmsc.epc.mnc001.mcc001.3gppnetwork.orgopen",
    );
    const sc = await routeRow("15550000000");
    expect(
      sc.compareDocumentPosition(def) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("creates a route to the SMSC peers", async () => {
    const requests = serve({ routes: [] });

    renderWithClient(<SMS />);

    expect(
      await screen.findByText(
        "No route: short messages from phones go nowhere.",
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create Route" }));

    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).queryByRole("checkbox", { name: /^core\./ }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Create" }),
    ).toBeDisabled();

    fill(/^Service Centre/, "+1555");
    fireEvent.click(
      within(dialog).getByRole("checkbox", {
        name: "smsc.epc.mnc001.mcc001.3gppnetwork.org (priority 20)",
      }),
    );
    expect(
      within(dialog).getByRole("button", { name: "Create" }),
    ).toBeDisabled();

    fill(/^Service Centre/, "15550000000");
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "POST",
        path: "/api/v1/ip-sm-gw/routes",
        body: { service_centre: "15550000000", peers: [smsc.id] },
      },
    ]);
  });

  it("deletes a route", async () => {
    const requests = serve();

    renderWithClient(<SMS />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete route 15550000000" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({
      method: "DELETE",
      path: `/api/v1/ip-sm-gw/routes/${scRoute.id}`,
      body: undefined,
    });
  });

  it("points to the Cores page without an SMSC peer", async () => {
    serve({ peers: [peer()], routes: [] });

    renderWithClient(
      <MemoryRouter>
        <SMS />
      </MemoryRouter>,
    );

    expect(
      await screen.findByText(/No SMSC peer\. Create a peer with the SGd role/),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Cores" })).toHaveAttribute(
      "href",
      "/cores",
    );
    expect(screen.getByRole("button", { name: "Create Route" })).toBeDisabled();
  });

  it("offers no change to a Read Only user", async () => {
    serve();
    renderWithClient(<SMS />, readOnlyAuth);

    await routeRow("Default");
    expect(
      screen.queryByRole("button", { name: "Create Route" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^(Edit|Delete) / }),
    ).not.toBeInTheDocument();
  });
});
