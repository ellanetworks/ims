import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import Dashboard, { formatBytes } from "@/pages/Dashboard";
import type { CallRecordCounts } from "@/queries/callRecords";
import type { DiameterPeer } from "@/queries/diameter";
import { identity, operator, page, peer, sip } from "@/test/fixtures";
import { mcpttStatus } from "@/test/mcptt";
import { json, renderWithClient, stubApi } from "@/test/render";

const METRICS = `# HELP ellaims_registered_subscribers Subscribers registered now.
# TYPE ellaims_registered_subscribers gauge
ellaims_registered_subscribers 1234
ellaims_active_calls 7
ellaims_database_storage_bytes{file="main"} 1048576
ellaims_database_storage_bytes{file="wal"} 524288
process_resident_memory_bytes 6.7108864e+07
process_start_time_seconds 1.7e+09
ellaims_trunk_active_calls{trunk="telnyx"} 2
ellaims_trunk_active_calls{trunk="twilio"} 1
`;

const ALERT_COLOR = "rgb(211, 47, 47)";

const TRUNKS = [
  {
    id: "0199a4d2-7c1e-7b3a-9f00-000000000001",
    name: "Telnyx",
    status: { state: "up" },
  },
  {
    id: "0199a4d2-7c1e-7b3a-9f00-000000000002",
    name: "Twilio",
    status: { state: "down" },
  },
];

const counts: CallRecordCounts = {
  answered: 60,
  cancelled: 10,
  busy: 20,
  rejected: 0,
  no_answer: 5,
  unavailable: 0,
  failed: 5,
};

const smsc = peer({
  id: "0199a1b2-0000-7000-8000-000000000002",
  host: "smsc.node.epc.mnc001.mcc001.3gppnetwork.org",
  address: "192.0.2.7",
  applications: ["sgd"],
  service_centres: ["15550000000"],
  status: { state: "down", error: "connection refused" },
});

interface Setup {
  peers?: DiameterPeer[];
  number?: string;
  mcptt?: boolean;
  counts?: CallRecordCounts;
  metrics?: () => Response;
  trunks?: unknown[];
}

const serve = ({
  peers = [peer(), smsc],
  number = "15550000100",
  mcptt = true,
  counts: byOutcome = counts,
  metrics = () => new Response(METRICS, { status: 200 }),
  trunks = TRUNKS,
}: Setup = {}) => {
  const urls: URL[] = [];
  const track =
    (handler: (url: URL) => Response) =>
    (url: URL): Response => {
      urls.push(url);
      return handler(url);
    };
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/operator": () => json(200, { result: operator }),
        "/api/v1/sip": () => json(200, { result: sip }),
        "/api/v1/diameter": () => json(200, { result: identity }),
        "/api/v1/diameter/peers": () => json(200, { result: { items: peers } }),
        "/api/v1/ip-sm-gw": () =>
          json(200, {
            result: {
              number,
              name: "sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org",
            },
          }),
        "/api/v1/mcptt": () =>
          mcptt
            ? json(200, { result: mcpttStatus })
            : json(404, { error: "MCPTT is not enabled" }),
        "/api/v1/mcptt/registrations": track(() =>
          json(200, { result: page([{}], { per_page: 1, total_count: 12 }) }),
        ),
        "/api/v1/mcptt/calls": track(() =>
          json(200, { result: page([{}], { per_page: 1, total_count: 2 }) }),
        ),
        "/api/v1/metrics": metrics,
        "/api/v1/sip-trunks": () => json(200, { result: { items: trunks } }),
        "/api/v1/call-records/counts": track(() =>
          json(200, { result: byOutcome }),
        ),
      }),
    ),
  );
  return urls;
};

function Location() {
  const location = useLocation();
  return (
    <div data-testid="location">{location.pathname + location.search}</div>
  );
}

const renderDashboard = () =>
  renderWithClient(
    <MemoryRouter initialEntries={["/dashboard"]}>
      <Routes>
        <Route path="/dashboard" element={<Dashboard />} />
        <Route path="*" element={<Location />} />
      </Routes>
    </MemoryRouter>,
  );

const panel = (name: string) =>
  screen
    .getByRole("heading", { level: 3, name })
    .closest(".MuiCard-root") as HTMLElement;

const findPanel = async (name: string) => {
  await screen.findByRole("heading", { level: 3, name });
  return panel(name);
};

const isAlert = (element: HTMLElement) =>
  getComputedStyle(element).color === ALERT_COLOR;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Dashboard", () => {
  it("shows this IMS and its Diameter peers", async () => {
    serve();
    renderDashboard();

    expect(
      await screen.findByRole("heading", { level: 1, name: "Dashboard" }),
    ).toBeInTheDocument();
    expect(document.title).toBe("Dashboard · Ella IMS");

    const ims = await findPanel("This IMS");
    expect(await within(ims).findByText("001 / 01")).toBeInTheDocument();
    expect(ims).toHaveTextContent("ims.mnc001.mcc001.3gppnetwork.org");
    expect(within(ims).getByText("192.0.2.20")).toBeInTheDocument();
    expect(within(ims).getByText("2001:db8::20")).toBeInTheDocument();
    expect(within(ims).getByRole("link", { name: "This IMS" })).toHaveAttribute(
      "href",
      "/cores",
    );

    const peerCard = await findPanel("Diameter Peers");
    const peerCount = await within(peerCard).findByText("2");
    expect(isAlert(peerCount)).toBe(true);
    expect(peerCard.closest("a")).toHaveAttribute("href", "/cores");
  });

  it("shows the number of SIP trunks", async () => {
    serve();
    renderDashboard();

    const trunkCard = await findPanel("SIP Trunks");
    const trunkCount = await within(trunkCard).findByText("2");
    expect(isAlert(trunkCount)).toBe(true);
    expect(trunkCard.closest("a")).toHaveAttribute("href", "/sip-trunks");

    fireEvent.click(trunkCount);
    expect(await screen.findByTestId("location")).toHaveTextContent(
      "/sip-trunks",
    );
  });

  it("shows the counts without alert when all are up", async () => {
    serve({
      peers: [peer()],
      trunks: [TRUNKS[0], { ...TRUNKS[1], status: { state: "unknown" } }],
    });
    renderDashboard();

    const trunkCount = await within(await findPanel("SIP Trunks")).findByText(
      "2",
    );
    expect(isAlert(trunkCount)).toBe(false);
    const peerCount = await within(panel("Diameter Peers")).findByText("1");
    expect(isAlert(peerCount)).toBe(false);
  });

  it("counts the active trunk calls", async () => {
    serve();
    renderDashboard();

    const trunkCalls = await findPanel("Active Trunk Calls");
    expect(await within(trunkCalls).findByText("3")).toBeInTheDocument();
    expect(trunkCalls.closest("a")).toHaveAttribute("href", "/calls");

    fireEvent.click(within(trunkCalls).getByText("3"));
    expect(await screen.findByTestId("location")).toHaveTextContent("/calls");
  });

  it("hides the SIP trunks without trunks", async () => {
    serve({ trunks: [] });
    renderDashboard();

    await within(await findPanel("Active Calls")).findByText("7");
    await within(panel("Diameter Peers")).findByText("2");
    await waitFor(() =>
      expect(
        vi
          .mocked(fetch)
          .mock.calls.some(([url]) => String(url).endsWith("/sip-trunks")),
      ).toBe(true),
    );
    expect(
      screen.queryByRole("heading", { name: "SIP Trunks" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Active Trunk Calls" }),
    ).not.toBeInTheDocument();
  });

  it("counts no Diameter peers", async () => {
    serve({ peers: [] });
    renderDashboard();

    const peerCount = await within(
      await findPanel("Diameter Peers"),
    ).findByText("0");
    expect(isAlert(peerCount)).toBe(false);
  });

  it("shows traffic and system figures from the metrics", async () => {
    serve();
    renderDashboard();

    const registered = await findPanel("Registered Subscribers");
    expect(await within(registered).findByText("1,234")).toBeInTheDocument();
    expect(registered.closest("a")).toHaveAttribute("href", "/registrations");

    const active = panel("Active Calls");
    expect(within(active).getByText("7")).toBeInTheDocument();
    expect(active.closest("a")).toHaveAttribute("href", "/calls");

    expect(within(panel("Process Memory")).getByText("64 MiB")).toBeVisible();
    expect(within(panel("Database Size")).getByText("1.5 MiB")).toBeVisible();
    expect(within(panel("Up Since")).getByText(/2023/)).toBeVisible();
  });

  it("shows N/A for a metric the IMS does not expose", async () => {
    serve({ metrics: () => new Response("", { status: 200 }) });
    renderDashboard();

    const registered = await findPanel("Registered Subscribers");
    expect(await within(registered).findByText("N/A")).toBeInTheDocument();
  });

  it("says when the metrics fail to load", async () => {
    serve({ metrics: () => new Response("boom", { status: 500 }) });
    renderDashboard();

    const memory = await findPanel("Process Memory");
    expect(
      await within(memory).findByText("Failed to load data."),
    ).toBeInTheDocument();
    expect(
      await within(await findPanel("This IMS")).findByText("001 / 01"),
    ).toBeInTheDocument();
  });

  it("counts the calls of the last 24 hours by outcome", async () => {
    const urls = serve();
    const before = Date.now();
    renderDashboard();

    const table = await screen.findByRole("table", {
      name: "Calls in the last 24 hours",
    });
    const rows = await within(table).findAllByRole("row");
    expect(rows.slice(1).map((r) => r.textContent)).toEqual([
      "answered60",
      "cancelled10",
      "busy20",
      "no answer5",
      "failed5",
      "Total100",
    ]);

    const request = urls.find(
      (u) => u.pathname === "/api/v1/call-records/counts",
    );
    const start = Date.parse(request?.searchParams.get("start") ?? "");
    expect(before - start).toBeGreaterThanOrEqual(24 * 60 * 60 * 1000 - 1000);
    expect(before - start).toBeLessThanOrEqual(24 * 60 * 60 * 1000 + 1000);
    expect(request?.searchParams.has("end")).toBe(false);

    expect(
      within(panel("Calls in the Last 24 Hours")).getByRole("link", {
        name: "Calls in the Last 24 Hours",
      }),
    ).toHaveAttribute("href", "/calls?range=24h");
    expect(within(rows[3]).getByRole("link")).toHaveAttribute(
      "href",
      "/calls?outcome=busy&range=24h",
    );
  });

  it("always shows the answered and the failed calls", async () => {
    serve({ counts: { ...counts, answered: 0, failed: 0, busy: 3 } });
    renderDashboard();

    const table = await screen.findByRole("table", {
      name: "Calls in the last 24 hours",
    });
    await within(table).findByText("busy");
    expect(
      within(table)
        .getAllByRole("row")
        .slice(1)
        .map((r) => r.textContent),
    ).toEqual([
      "answered0",
      "cancelled10",
      "busy3",
      "no answer5",
      "failed0",
      "Total18",
    ]);
  });

  it("opens the calls of an outcome when its row is clicked", async () => {
    serve();
    renderDashboard();

    const table = await screen.findByRole("table", {
      name: "Calls in the last 24 hours",
    });
    fireEvent.click(await within(table).findByText("60"));

    expect(await screen.findByTestId("location")).toHaveTextContent(
      "/calls?outcome=answered&range=24h",
    );
  });

  it("shows the services that are enabled", async () => {
    const urls = serve();
    renderDashboard();

    const sms = await findPanel("SMS over IP");
    expect(await within(sms).findByText("Enabled")).toBeInTheDocument();
    expect(within(sms).getByText("15550000100")).toBeInTheDocument();
    expect(sms).toHaveTextContent(
      "smsc.node.epc.mnc001.mcc001.3gppnetwork.org",
    );
    expect(within(sms).getByText("down")).toBeInTheDocument();

    const ptt = await findPanel("Push-to-Talk");
    expect(await within(ptt).findByText("Enabled")).toBeInTheDocument();
    expect(await within(ptt).findByText("12")).toBeInTheDocument();
    expect(within(ptt).getByText("2")).toBeInTheDocument();
    expect(
      within(ptt).getByRole("link", { name: "Push-to-Talk" }),
    ).toHaveAttribute("href", "/ptt");
    expect(
      urls
        .filter((u) => u.pathname.startsWith("/api/v1/mcptt/"))
        .every((u) => u.searchParams.get("per_page") === "1"),
    ).toBe(true);
  });

  it("shows the services that are disabled", async () => {
    const urls = serve({
      number: "",
      mcptt: false,
    });
    renderDashboard();

    const sms = await findPanel("SMS over IP");
    expect(await within(sms).findByText("Disabled")).toBeInTheDocument();
    expect(
      within(sms).getByText("No IP-SM-GW number: phones get no SMS over IP."),
    ).toBeInTheDocument();

    const ptt = await findPanel("Push-to-Talk");
    expect(await within(ptt).findByText("Disabled")).toBeInTheDocument();
    expect(ptt).toHaveTextContent("No mcptt section in ims.yaml.");
    expect(urls.some((u) => u.pathname.startsWith("/api/v1/mcptt/"))).toBe(
      false,
    );
  });

  it("warns when SMS over IP has no SMSC peer", async () => {
    serve({ peers: [peer()] });
    renderDashboard();

    const sms = await findPanel("SMS over IP");
    expect(await within(sms).findByText("no SMSC peer")).toBeInTheDocument();
  });
});

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1023, "1,023 B"],
    [1536, "1.5 KiB"],
    [64 * 1024 * 1024, "64 MiB"],
    [3 * 1024 ** 5, "3,072 TiB"],
  ])("formats %d as %s", (bytes, want) => {
    expect(formatBytes(bytes)).toBe(want);
  });
});
