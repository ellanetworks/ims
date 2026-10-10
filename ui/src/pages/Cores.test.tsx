import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Cores from "@/pages/Cores";
import type { DiameterPeer, DiameterRoute } from "@/queries/diameter";
import type { PolicyWithStatus } from "@/queries/policy";
import { identity, peer, policy, route as routeOf, sip } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

interface Request {
  method: string;
  path: string;
  body?: unknown;
}

const serve = ({
  listeners = sip.listeners,
  peers = [peer()],
  routes = [routeOf(), routeOf({ application: "rx" })],
  current = policy(),
  answer,
}: {
  listeners?: typeof sip.listeners;
  peers?: DiameterPeer[];
  routes?: DiameterRoute[];
  current?: PolicyWithStatus;
  answer?: (req: Request) => Response | undefined;
} = {}) => {
  const requests: Request[] = [];
  const record = (url: URL, init?: RequestInit) => {
    const req: Request = {
      method: init?.method ?? "GET",
      path: url.pathname,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    if (req.method !== "GET") requests.push(req);
    return req;
  };
  const route =
    (fallback: (req: Request) => Response) =>
    (url: URL, init?: RequestInit) => {
      const req = record(url, init);
      return (req.method !== "GET" && answer?.(req)) || fallback(req);
    };

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/sip": () => json(200, { result: { ...sip, listeners } }),
        "/api/v1/diameter": () => json(200, { result: identity }),
        "/api/v1/diameter/peers": route((req) =>
          req.method === "POST"
            ? json(201, { result: { ...peer(), ...(req.body as object) } })
            : json(200, { result: { items: peers } }),
        ),
        [`/api/v1/diameter/peers/${peer().id}`]: route((req) =>
          req.method === "DELETE"
            ? json(200, { result: { message: "Diameter peer deleted" } })
            : json(200, { result: { ...peer(), ...(req.body as object) } }),
        ),
        "/api/v1/diameter/routes": () =>
          json(200, { result: { items: routes } }),
        ...Object.fromEntries(
          routes.map((r) => [
            `/api/v1/diameter/routes/${r.application}`,
            route((req) =>
              json(200, { result: { ...r, ...(req.body as object) } }),
            ),
          ]),
        ),
        "/api/v1/policy": route((req) =>
          req.method === "PUT"
            ? json(200, {
                result: { ...(req.body as object), status: current.status },
              })
            : json(200, { result: current }),
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

const peerRow = async (host: string) =>
  (await screen.findByText(host)).closest('[role="row"]') as HTMLElement;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Cores", () => {
  it("shows what to set in the core", async () => {
    serve();

    renderWithClient(<Cores />);

    await screen.findByText(identity.host);
    expect(settingRows("IMS identity")).toEqual([
      ["P-CSCF Addresses", "192.0.2.202001:db8::20"],
      ["Diameter Host", identity.host],
      ["Diameter Realm", identity.realm],
    ]);
  });

  it("flags a P-CSCF port phones do not expect", async () => {
    serve({
      listeners: [
        { role: "pcscf", address: "192.0.2.20:5070", transports: ["udp"] },
      ],
    });

    renderWithClient(<Cores />);

    expect(await screen.findByText("port 5070")).toBeInTheDocument();
  });

  it("does not flag the default SIP port", async () => {
    serve();

    renderWithClient(<Cores />);

    await screen.findByText(identity.host);
    expect(screen.queryByText(/^port /)).not.toBeInTheDocument();
  });

  it("copies a P-CSCF address", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    serve();

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Copy 2001:db8::20" }),
    );

    expect(writeText).toHaveBeenCalledWith("2001:db8::20");
  });

  it("lists the peers with their roles and state", async () => {
    serve();

    renderWithClient(<Cores />);

    expect(
      await screen.findByRole("heading", { level: 2, name: "Peers (1)" }),
    ).toBeInTheDocument();
    const row = await peerRow(peer().host);
    expect(
      within(row)
        .getAllByRole("gridcell")
        .map((cell) => cell.textContent),
    ).toEqual([
      "core.epc.mnc001.mcc001.3gppnetwork.org",
      "epc.mnc001.mcc001.3gppnetwork.org",
      "192.0.2.1:3868",
      "SCTP",
      "HSS, PCRF",
      "10",
      "open",
      "2026-10-08 12:00:00",
      "",
    ]);
  });

  it("adds a peer", async () => {
    const requests = serve({ peers: [] });

    renderWithClient(<Cores />);
    expect(await screen.findByText("No peers.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add Peer" }));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "Add" })).toBeDisabled();
    expect(within(dialog).getByRole("textbox", { name: "Port" })).toHaveValue(
      "3868",
    );
    expect(within(dialog).getByRole("checkbox", { name: "HSS" })).toBeChecked();
    expect(
      within(dialog).getByRole("checkbox", { name: "PCRF" }),
    ).toBeChecked();

    fill(/^Host/, "hss.example.org");
    fill(/^Address/, "192.0.2.9");
    fill(/^Priority/, "1");
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "PCRF" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Diameter and SIP restart",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "POST",
        path: "/api/v1/diameter/peers",
        body: {
          host: "hss.example.org",
          address: "192.0.2.9",
          port: 3868,
          transport: "tcp",
          applications: ["cx"],
          priority: 1,
        },
      },
    ]);
  });

  it("adds a peer without a host", async () => {
    const requests = serve({ peers: [] });

    renderWithClient(<Cores />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Peer" }));
    fill(/^Address/, "192.0.2.9");
    fireEvent.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0].body).toMatchObject({ host: "", address: "192.0.2.9" });
  });

  it("shows the host a peer without one gave", async () => {
    const learned = "mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org";
    serve({
      peers: [peer({ host: "", status: { ...peer().status, host: learned } })],
    });

    renderWithClient(<Cores />);

    const row = await peerRow(learned);
    expect(
      within(row).getByRole("button", { name: `Edit ${learned}` }),
    ).toBeInTheDocument();
  });

  it("shows why a peer is down", async () => {
    const error =
      "peer answered as mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org, expected hss.example.org";
    serve({
      peers: [peer({ status: { ...peer().status, state: "down", error } })],
    });

    renderWithClient(<Cores />);
    fireEvent.mouseOver(await screen.findByText("down"));

    expect(await screen.findByRole("tooltip")).toHaveTextContent(error);
  });

  it("requires a role, a valid port and a valid priority", async () => {
    serve({ peers: [] });

    renderWithClient(<Cores />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Peer" }));
    fill(/^Host/, "hss.example.org");
    fill(/^Address/, "192.0.2.9");

    fill(/^Port/, "70000");
    expect(screen.getByText("1 to 65535")).toBeInTheDocument();
    fill(/^Port/, "3868");
    fill(/^Priority/, "65536");
    expect(screen.getByText("0 to 65535")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
    fill(/^Priority/, "10");
    fireEvent.click(screen.getByRole("checkbox", { name: "HSS" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "PCRF" }));
    expect(screen.getByText("At least one")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  });

  it("moves or renames a peer without warning, keeping its roles", async () => {
    const stored = peer({ applications: ["rx", "cx"] });
    const requests = serve({ peers: [stored] });

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: `Edit ${stored.host}` }),
    );
    fill(/^Address/, "192.0.2.2");
    fill(/^Host/, "hss.example.org");
    fill(/^Priority/, "1");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({
      method: "PUT",
      path: `/api/v1/diameter/peers/${stored.id}`,
      body: {
        host: "hss.example.org",
        address: "192.0.2.2",
        port: 3868,
        transport: "sctp",
        applications: ["rx", "cx"],
        priority: 1,
      },
    });
  });

  it("warns when an edit changes the transports and restarts Diameter and SIP", async () => {
    serve();

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: `Edit ${peer().host}` }),
    );
    fireEvent.mouseDown(screen.getByRole("combobox", { name: "Transport" }));
    fireEvent.click(screen.getByRole("option", { name: "TCP" }));

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Diameter and SIP restart",
    );
  });

  it("deletes a peer without warning when another uses its transport", async () => {
    const other = peer({
      id: "0199a1b2-0000-7000-8000-000000000002",
      host: "hss2.example.org",
    });
    serve({ peers: [peer(), other] });

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: `Delete ${peer().host}` }),
    );

    expect(
      within(screen.getByRole("dialog")).queryByRole("alert"),
    ).not.toBeInTheDocument();
  });

  it("shows where Cx and Rx requests go", async () => {
    serve({
      routes: [
        routeOf(),
        routeOf({
          application: "rx",
          realm: "epc.mnc001.mcc001.3gppnetwork.org",
          destination_realm: "epc.mnc001.mcc001.3gppnetwork.org",
        }),
      ],
    });

    renderWithClient(<Cores />);

    await waitFor(() =>
      expect(settingRows("Routes")).toEqual([
        ["HSS Realm", "ims.mnc001.mcc001.3gppnetwork.orghome domain"],
        ["PCRF Realm", "epc.mnc001.mcc001.3gppnetwork.org"],
      ]),
    );
  });

  it("sets the realm of the PCRF", async () => {
    const requests = serve();

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit PCRF Realm" }),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    fill(/^Realm/, "bad realm");
    expect(screen.getByText("A domain name")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fill(/^Realm/, "epc.mnc001.mcc001.3gppnetwork.org");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "PUT",
        path: "/api/v1/diameter/routes/rx",
        body: { realm: "epc.mnc001.mcc001.3gppnetwork.org" },
      },
    ]);
  });

  it("deletes a peer", async () => {
    const requests = serve();

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: `Delete ${peer().host}` }),
    );
    const dialog = screen.getByRole("dialog", {
      name: `Delete ${peer().host}?`,
    });
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Diameter and SIP restart",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      { method: "DELETE", path: `/api/v1/diameter/peers/${peer().id}` },
    ]);
  });

  it("shows why a peer cannot be deleted", async () => {
    serve({
      answer: (req) =>
        req.method === "DELETE"
          ? json(409, { error: "rx requires a Diameter peer serving rx" })
          : undefined,
    });

    renderWithClient(<Cores />);
    fireEvent.click(
      await screen.findByRole("button", { name: `Delete ${peer().host}` }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(
      await screen.findByText(
        "Could not delete: rx requires a Diameter peer serving rx",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("shows no voice QoS", async () => {
    serve();

    renderWithClient(<Cores />);

    await waitFor(() =>
      expect(settingRows("Voice QoS")).toEqual([["Policy Function", "None"]]),
    );
  });

  it("shows the state of the PCRF", async () => {
    serve({
      routes: [
        routeOf(),
        routeOf({
          application: "rx",
          peers: [{ ...routeOf().peers[0], status: { state: "down" } }],
        }),
      ],
      current: policy({
        interface: "rx",
        status: { interface: "rx", endpoint: peer().host },
      }),
    });

    renderWithClient(<Cores />);

    await waitFor(() =>
      expect(settingRows("Voice QoS")).toEqual([
        ["Policy Function", "PCRF"],
        ["Status", "down"],
      ]),
    );
  });

  it("shows the last request to the PCF", async () => {
    serve({
      current: policy({
        interface: "n5",
        n5: { pcf_uri: "https://pcf.example.org" },
        status: {
          interface: "n5",
          endpoint: "https://pcf.example.org",
          notify: "http://192.0.2.20:8080",
          last: {
            at: "2026-10-08T12:00:00.000Z",
            reachable: true,
            result: "201 Created",
          },
        },
      }),
    });

    renderWithClient(<Cores />);

    await waitFor(() =>
      expect(settingRows("Voice QoS")).toEqual([
        ["Policy Function", "PCF"],
        ["PCF URI", "https://pcf.example.org"],
        ["Notification URI", "http://192.0.2.20:8080"],
        ["Status", "reachable201 Created2026-10-08 12:00:00"],
      ]),
    );
  });

  it("shows a policy function being applied", async () => {
    serve({
      current: policy({ interface: "rx", status: { interface: "none" } }),
    });

    renderWithClient(<Cores />);

    await waitFor(() =>
      expect(settingRows("Voice QoS")).toContainEqual(["Status", "applying"]),
    );
  });

  it("switches voice QoS to a PCF", async () => {
    const requests = serve();

    renderWithClient(<Cores />);
    await waitFor(() =>
      expect(settingRows("Voice QoS")).toEqual([["Policy Function", "None"]]),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Policy Function" }),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: "PCF" }));
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();
    fill(/^PCF URI/, "pcf.example.org");
    expect(
      screen.getByText("http[s]://host[:port][/prefix]"),
    ).toBeInTheDocument();
    fill(/^PCF URI/, "https://pcf.example.org");
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Diameter and SIP restart",
    );
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "PUT",
        path: "/api/v1/policy",
        body: { interface: "n5", n5: { pcf_uri: "https://pcf.example.org" } },
      },
    ]);
  });
});
