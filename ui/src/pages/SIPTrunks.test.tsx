import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import SIPTrunks from "@/pages/SIPTrunks";
import type { SIPRoute, SIPTrunk } from "@/queries/sipTrunks";
import { json, readOnlyAuth, renderWithClient, stubApi } from "@/test/render";

interface Request {
  method: string;
  path: string;
  body?: unknown;
}

const TELNYX_ID = "0199a1b2-0000-7000-8000-0000000000a1";
const TWILIO_ID = "0199a1b2-0000-7000-8000-0000000000a2";
const ROUTE_ID = "0199a1b2-0000-7000-8000-0000000000b1";
const ROUTE_33_ID = "0199a1b2-0000-7000-8000-0000000000b2";
const ROUTE_1_ID = "0199a1b2-0000-7000-8000-0000000000b3";

const CA_PEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----";

const PIN_PEM = "-----BEGIN CERTIFICATE-----\nMIIC\n-----END CERTIFICATE-----";

const certificate = {
  subject: "CN=sip.telnyx.com,O=Telnyx",
  issuer: "CN=Telnyx CA",
  dns_names: ["sip.telnyx.com"],
  uris: [],
  ip_addresses: [],
  identities: ["sip.telnyx.com"],
  ca: true,
  not_before: "2026-01-01T00:00:00Z",
  not_after: "2027-01-01T00:00:00Z",
  sha256_fingerprint: "2C:54:DF:66",
};

const telnyx = (overrides: Partial<SIPTrunk> = {}): SIPTrunk => ({
  id: TELNYX_ID,
  name: "Telnyx",
  addresses: [
    { host: "sip.telnyx.com", port: 5061, transport: "tls" },
    { host: "192.0.2.10" },
  ],
  inbound_cidrs: ["192.76.120.10/32"],
  domain: "sip.telnyx.com",
  trusted: false,
  keepalive_interval: 30,
  enabled: true,
  tls: {
    required: false,
    ca_certificates: CA_PEM,
    identities: ["sip.telnyx.com"],
    ca_certificate_details: [certificate],
  },
  effective: {
    domain: "sip.telnyx.com",
    inbound_cidrs: ["192.76.120.10/32"],
    tls_identities: ["sip.telnyx.com"],
  },
  status: { state: "up", since: "2026-11-01T00:00:00Z" },
  ...overrides,
});

const twilio = (): SIPTrunk => ({
  id: TWILIO_ID,
  name: "Twilio",
  addresses: [{ host: "ims.pstn.twilio.com", transport: "tcp" }],
  inbound_cidrs: [],
  domain: "ims.pstn.twilio.com",
  trusted: true,
  keepalive_interval: 0,
  enabled: false,
  tls: { required: false },
  effective: {
    domain: "ims.pstn.twilio.com",
    inbound_cidrs: [],
    tls_identities: ["ims.pstn.twilio.com"],
  },
  status: { state: "unknown" },
});

const defaultRoute: SIPRoute = {
  id: ROUTE_ID,
  prefix: "",
  trunks: [TELNYX_ID, TWILIO_ID],
};

const serve = ({
  trunks = [telnyx(), twilio()],
  routes = [defaultRoute],
  answer,
}: {
  trunks?: SIPTrunk[];
  routes?: SIPRoute[];
  answer?: (req: Request) => Response | undefined;
} = {}) => {
  const requests: Request[] = [];
  const handle =
    (fallback: (req: Request) => Response) =>
    (url: URL, init?: RequestInit) => {
      const req: Request = {
        method: init?.method ?? "GET",
        path: url.pathname,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      if (req.method === "GET") return fallback(req);
      requests.push(req);
      return answer?.(req) ?? fallback(req);
    };

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/sip-trunks": handle((req) =>
          req.method === "POST"
            ? json(201, { result: { ...twilio(), ...(req.body as object) } })
            : json(200, { result: { items: trunks } }),
        ),
        ...Object.fromEntries(
          trunks.map((t) => [
            `/api/v1/sip-trunks/${t.id}`,
            handle((req) =>
              req.method === "DELETE"
                ? json(200, { result: { message: "SIP trunk deleted" } })
                : json(200, { result: { ...t, ...(req.body as object) } }),
            ),
          ]),
        ),
        "/api/v1/sip-routes": handle((req) =>
          req.method === "POST"
            ? json(201, {
                result: { id: ROUTE_ID, ...(req.body as object) },
              })
            : json(200, { result: { items: routes } }),
        ),
        ...Object.fromEntries(
          routes.map((r) => [
            `/api/v1/sip-routes/${r.id}`,
            handle((req) =>
              req.method === "DELETE"
                ? json(200, { result: { message: "SIP route deleted" } })
                : json(200, { result: { ...r, ...(req.body as object) } }),
            ),
          ]),
        ),
      }),
    ),
  );
  return requests;
};

const fill = (label: string, value: string, index = 0) =>
  fireEvent.change(screen.getAllByRole("textbox", { name: label })[index], {
    target: { value },
  });

const cells = (row: HTMLElement) =>
  within(row)
    .getAllByRole("gridcell")
    .map((cell) => cell.textContent);

const rowOf = async (text: string) =>
  (await screen.findByText(text)).closest('[role="row"]') as HTMLElement;

const choose = (label: string, option: string, index = 0) => {
  fireEvent.mouseDown(
    within(screen.getByRole("dialog")).getAllByRole("combobox", {
      name: label,
    })[index],
  );
  fireEvent.click(screen.getByRole("option", { name: option }));
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SIP Trunks", () => {
  it("lists the trunks and the routes", async () => {
    serve();

    renderWithClient(<SIPTrunks />);

    expect(
      await screen.findByRole("heading", { level: 2, name: "Trunks (2)" }),
    ).toBeInTheDocument();
    expect(cells(await rowOf("Telnyx"))).toEqual([
      "Telnyx",
      "up",
      "Yes",
      "No",
      "sip.telnyx.com:5061 (TLS)192.0.2.10",
      "",
    ]);
    expect(cells(await rowOf("Twilio"))).toEqual([
      "Twilio",
      "unknown",
      "No",
      "Yes",
      "ims.pstn.twilio.com (TCP)",
      "",
    ]);
    expect(
      screen.getByRole("heading", { level: 2, name: "Routes (1)" }),
    ).toBeInTheDocument();
    expect(cells(await rowOf("Default"))).toEqual([
      "Default",
      "Telnyx, Twilio",
      "",
    ]);
  });

  it("sorts the routes by prefix with the default last", async () => {
    serve({
      routes: [
        defaultRoute,
        { id: ROUTE_33_ID, prefix: "+33", trunks: [TWILIO_ID] },
        { id: ROUTE_1_ID, prefix: "+1", trunks: [TELNYX_ID] },
      ],
    });

    renderWithClient(<SIPTrunks />);

    await rowOf("Default");
    expect(
      within(screen.getByRole("grid", { name: "Routes (3)" }))
        .getAllByRole("row")
        .slice(1)
        .map((row) => within(row).getAllByRole("gridcell")[0].textContent),
    ).toEqual(["+1", "+33", "Default"]);
  });

  it("offers no change to a Read Only user", async () => {
    serve();

    renderWithClient(<SIPTrunks />, readOnlyAuth);

    expect(cells(await rowOf("Telnyx"))).toHaveLength(5);
    await rowOf("Default");
    expect(
      screen.queryByRole("button", { name: /^Create / }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^(Edit|Delete) / }),
    ).not.toBeInTheDocument();
  });

  it("creates a trunk", async () => {
    const requests = serve({ trunks: [], routes: [] });

    renderWithClient(<SIPTrunks />);
    expect(await screen.findByText("No trunks.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Route" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Create Trunk" }));

    const dialog = screen.getByRole("dialog");
    const create = within(dialog).getByRole("button", { name: "Create" });
    expect(create).toBeDisabled();
    expect(
      within(dialog).getByRole("textbox", { name: "Keepalive Interval (s)" }),
    ).toHaveValue("30");
    expect(
      within(dialog).getByRole("checkbox", { name: "Enabled" }),
    ).toBeChecked();

    fill("Name", "Carrier");
    fill("Host", "sip.carrier.example");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Add Address" }),
    );
    fill("Host", "192.0.2.50", 1);
    fill("Port", "70000", 1);
    expect(create).toBeDisabled();
    fill("Port", "5070", 1);
    choose("Transport", "TCP", 1);
    fill("Inbound CIDRs", "192.0.2.0/24\n198.51.100.7");
    fill("Keepalive Interval (s)", "3");
    expect(within(dialog).getByText("0, or 5 to 3600")).toBeInTheDocument();
    expect(create).toBeDisabled();
    fill("Keepalive Interval (s)", "60");
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Trusted" }));
    fireEvent.click(create);

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "POST",
        path: "/api/v1/sip-trunks",
        body: {
          name: "Carrier",
          addresses: [
            { host: "sip.carrier.example" },
            { host: "192.0.2.50", port: 5070, transport: "tcp" },
          ],
          inbound_cidrs: ["192.0.2.0/24", "198.51.100.7"],
          trusted: true,
          keepalive_interval: 60,
          enabled: true,
        },
      },
    ]);
  });

  it("edits a trunk and shows its certificates", async () => {
    const requests = serve();

    renderWithClient(<SIPTrunks />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Telnyx" }));

    const dialog = screen.getByRole("dialog", { name: "Edit Trunk" });
    fireEvent.click(within(dialog).getByRole("button", { name: "TLS" }));
    expect(
      within(dialog).getByText("CN=sip.telnyx.com,O=Telnyx"),
    ).toBeVisible();
    expect(within(dialog).getByText("2C:54:DF:66")).toBeVisible();

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove address 2" }),
    );
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Enabled" }));
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Required" }));
    fill("Pins", `AA:BB\n${PIN_PEM}\nCC:DD`);
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "PUT",
        path: `/api/v1/sip-trunks/${TELNYX_ID}`,
        body: {
          name: "Telnyx",
          addresses: [{ host: "sip.telnyx.com", port: 5061, transport: "tls" }],
          inbound_cidrs: ["192.76.120.10/32"],
          domain: "sip.telnyx.com",
          trusted: false,
          keepalive_interval: 30,
          enabled: false,
          tls: {
            required: true,
            ca_certificates: CA_PEM,
            pins: [PIN_PEM, "AA:BB", "CC:DD"],
            identities: ["sip.telnyx.com"],
          },
        },
      },
    ]);
  });

  it("shows the values in effect where the trunk leaves the defaults", async () => {
    serve({
      trunks: [
        telnyx({
          addresses: [
            { host: "sip.telnyx.com", port: 5061, transport: "tls" },
            { host: "192.0.2.10" },
            { host: "telnyx.example" },
          ],
          domain: "",
          inbound_cidrs: [],
          tls: { required: false, ca_certificates: CA_PEM },
        }),
      ],
    });

    renderWithClient(<SIPTrunks />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Telnyx" }));

    const dialog = screen.getByRole("dialog", { name: "Edit Trunk" });
    fireEvent.click(within(dialog).getByRole("button", { name: "TLS" }));
    const field = (name: string) =>
      within(dialog).getByRole("textbox", { name });
    expect(field("Domain")).toHaveValue("");
    expect(field("Domain")).toHaveAttribute("placeholder", "sip.telnyx.com");
    expect(field("Inbound CIDRs")).toHaveAttribute(
      "placeholder",
      "192.0.2.10/32",
    );
    expect(field("Identities")).toHaveAttribute(
      "placeholder",
      "sip.telnyx.com\ntelnyx.example",
    );
  });

  it("derives the defaults from the form", async () => {
    serve();

    renderWithClient(<SIPTrunks />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Telnyx" }));

    const dialog = screen.getByRole("dialog", { name: "Edit Trunk" });
    fireEvent.click(within(dialog).getByRole("button", { name: "TLS" }));
    const field = (name: string) =>
      within(dialog).getByRole("textbox", { name });
    const hosts = within(dialog).getAllByRole("textbox", { name: "Host" });
    fireEvent.change(hosts[0], { target: { value: "sip.example.net" } });
    fireEvent.change(hosts[1], { target: { value: "2001:db8::1" } });
    for (const name of ["Domain", "Inbound CIDRs", "Identities"]) {
      fireEvent.change(field(name), { target: { value: "" } });
    }

    expect(field("Domain")).toHaveAttribute("placeholder", "sip.example.net");
    expect(field("Inbound CIDRs")).toHaveAttribute(
      "placeholder",
      "2001:db8::1/128",
    );
    expect(field("Identities")).toHaveAttribute(
      "placeholder",
      "sip.example.net",
    );

    fireEvent.change(field("Domain"), { target: { value: "example.net" } });
    expect(field("Identities")).toHaveAttribute(
      "placeholder",
      "example.net\nsip.example.net",
    );

    fireEvent.change(field("CA Certificates"), { target: { value: "" } });
    expect(field("Identities")).not.toHaveAttribute("placeholder");
  });

  it("shows no default where the trunk sets the value", async () => {
    serve();

    renderWithClient(<SIPTrunks />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Telnyx" }));

    const dialog = screen.getByRole("dialog", { name: "Edit Trunk" });
    fireEvent.click(within(dialog).getByRole("button", { name: "TLS" }));
    for (const name of ["Domain", "Inbound CIDRs", "Identities"]) {
      expect(within(dialog).getByRole("textbox", { name })).not.toHaveAttribute(
        "placeholder",
      );
    }
  });

  it("shows the server error and keeps the form", async () => {
    serve({
      answer: (req) =>
        req.method === "PUT"
          ? json(400, { error: "tls requires tls.ca_certificates" })
          : undefined,
    });

    renderWithClient(<SIPTrunks />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Twilio" }));
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not update the trunk: tls requires tls.ca_certificates",
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("deletes a trunk", async () => {
    const requests = serve({ routes: [] });

    renderWithClient(<SIPTrunks />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Twilio" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete Trunk" });
    expect(dialog).toHaveTextContent("Delete Twilio?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      { method: "DELETE", path: `/api/v1/sip-trunks/${TWILIO_ID}` },
    ]);
  });

  it("shows why a trunk in a route cannot be deleted", async () => {
    serve({
      answer: (req) =>
        req.method === "DELETE"
          ? json(409, { error: "SIP trunk is used by a route" })
          : undefined,
    });

    renderWithClient(<SIPTrunks />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Telnyx" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(
      await screen.findByText(
        "Failed to delete trunk: SIP trunk is used by a route",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("creates a route with its trunks in order", async () => {
    const requests = serve({ routes: [] });

    renderWithClient(<SIPTrunks />);
    expect(await screen.findByText("No routes.")).toBeInTheDocument();
    await rowOf("Telnyx");
    fireEvent.click(screen.getByRole("button", { name: "Create Route" }));

    const dialog = screen.getByRole("dialog", { name: "Create Route" });
    const create = within(dialog).getByRole("button", { name: "Create" });
    expect(create).toBeDisabled();

    fill("Prefix", "+0");
    expect(
      within(dialog).getByText("+ and 1 to 15 digits, not starting with 0"),
    ).toBeInTheDocument();
    fill("Prefix", "+1");
    choose("Add Trunk", "Telnyx");
    choose("Add Trunk", "Twilio");
    expect(
      within(dialog)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["1. Telnyx", "2. Twilio"]);
    expect(
      within(dialog).getByRole("button", { name: "Move Twilio down" }),
    ).toBeDisabled();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move Twilio up" }),
    );
    expect(
      within(dialog)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["1. Twilio", "2. Telnyx"]);
    fireEvent.click(create);

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "POST",
        path: "/api/v1/sip-routes",
        body: { prefix: "+1", trunks: [TWILIO_ID, TELNYX_ID] },
      },
    ]);
  });

  it("edits the default route", async () => {
    const requests = serve();

    renderWithClient(<SIPTrunks />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit route Default" }),
    );

    const dialog = screen.getByRole("dialog", { name: "Edit Route" });
    expect(within(dialog).getByRole("textbox", { name: "Prefix" })).toHaveValue(
      "",
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove Telnyx" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      {
        method: "PUT",
        path: `/api/v1/sip-routes/${ROUTE_ID}`,
        body: { prefix: "", trunks: [TWILIO_ID] },
      },
    ]);
  });

  it("deletes a route", async () => {
    const requests = serve();

    renderWithClient(<SIPTrunks />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete route Default" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete Route" });
    expect(dialog).toHaveTextContent("Delete route Default?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(requests).toEqual([
      { method: "DELETE", path: `/api/v1/sip-routes/${ROUTE_ID}` },
    ]);
  });
});
