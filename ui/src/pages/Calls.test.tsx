import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import Calls, { CALLS_REFRESH_MS } from "@/pages/Calls";
import type { CallRecord } from "@/queries/callRecords";
import { callRecord } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const answered = callRecord();

const busy = callRecord({
  id: 2,
  icid: "B2",
  calling_party: ["tel:+15551230003"],
  called_party: "tel:+15551230001",
  requested_at: "2026-10-08T12:05:00.000Z",
  delivery_end_at: undefined,
  sip_status: 486,
  outcome: "busy",
  ended_by: "callee",
  media: [],
  duration_ms: undefined,
});

const ringing = callRecord({
  id: 3,
  icid: "C3",
  requested_at: "2026-10-08T12:10:00.000Z",
  delivery_start_at: undefined,
  delivery_end_at: undefined,
  sip_status: undefined,
  outcome: undefined,
  ended_by: undefined,
  media: [],
  in_progress: true,
  duration_ms: undefined,
});

const serve = (items: CallRecord[], details: CallRecord[] = items) => {
  const urls: string[] = [];
  const puts: unknown[] = [];
  let days = 90;

  const detailRoutes = Object.fromEntries(
    details.map((r) => [
      `/api/v1/call-records/${r.id}`,
      () => json(200, { result: r }),
    ]),
  );

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        ...detailRoutes,
        "/api/v1/call-records": (url) => {
          urls.push(url.search);
          const outcomes = url.searchParams.getAll("outcome");
          const found = items.filter(
            (r) =>
              outcomes.length === 0 ||
              (r.outcome !== undefined && outcomes.includes(r.outcome)),
          );
          return json(200, {
            result: {
              items: found,
              page: 1,
              per_page: 25,
              total_count: found.length,
            },
          });
        },
        "/api/v1/call-records/retention": (_url, init) => {
          if (init?.method === "PUT") {
            const body = JSON.parse(String(init.body));
            puts.push(body);
            days = body.days;
          }
          return json(200, { result: { days } });
        },
      }),
    ),
  );

  return { urls, puts };
};

const cells = (text: string) =>
  within(screen.getAllByText(text)[0].closest('[role="row"]') as HTMLElement)
    .getAllByRole("gridcell")
    .map((cell) => cell.textContent);

const renderCalls = (path = "/calls") =>
  renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <Calls />
    </MemoryRouter>,
  );

const lastQuery = (urls: string[]) => new URLSearchParams(urls.at(-1));

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Calls", () => {
  it("lists the calls", async () => {
    serve([ringing, busy, answered]);

    renderCalls();

    expect(
      await screen.findByRole("heading", { level: 1, name: "Calls (3)" }),
    ).toBeInTheDocument();
    await screen.findByText("2026-10-08 12:00:00");

    expect(cells("2026-10-08 12:00:00")).toEqual([
      "2026-10-08 12:00:00",
      "+15551230001",
      "+15551230002",
      "answered",
      "1:05",
      "audio, video",
    ]);
    expect(cells("2026-10-08 12:05:00")).toEqual([
      "2026-10-08 12:05:00",
      "+15551230003",
      "+15551230001",
      "busy",
      "—",
      "—",
    ]);
    expect(cells("2026-10-08 12:10:00")[3]).toBe("ringing");
  });

  it("shows a call not yet alerted as calling", async () => {
    serve([{ ...ringing, alerted: false }]);

    renderCalls();

    await screen.findByText("2026-10-08 12:10:00");
    expect(cells("2026-10-08 12:10:00")[3]).toBe("calling");
  });

  it("lists the calls of the last 24 hours by default", async () => {
    const { urls } = serve([answered]);
    const before = Date.now();

    renderCalls();
    await screen.findByText("2026-10-08 12:00:00");

    expect(
      screen.getByRole("button", { name: "Time range: Last 24 hours" }),
    ).toBeInTheDocument();

    const start = new Date(lastQuery(urls).get("start") ?? "").getTime();
    expect(start).toBeGreaterThanOrEqual(before - 24 * 60 * 60_000 - 1000);
    expect(start).toBeLessThanOrEqual(Date.now() - 24 * 60 * 60_000);
    expect(lastQuery(urls).has("end")).toBe(false);
  });

  it("filters from the URL, including the minute of the end", async () => {
    const { urls } = serve([busy, answered]);

    renderCalls(
      "/calls?range=custom&start=2026-10-08T12:00:00.000Z&end=2026-10-08T12:31:00.000Z&search=alice&outcome=busy",
    );
    await screen.findByText("2026-10-08 12:05:00");

    const q = lastQuery(urls);
    expect(q.get("start")).toBe("2026-10-08T12:00:00.000Z");
    expect(q.get("end")).toBe("2026-10-08T12:31:00.000Z");
    expect(q.get("search")).toBe("alice");
    expect(q.getAll("outcome")).toEqual(["busy"]);
  });

  it("ends yesterday at the local midnight that follows it", async () => {
    const { urls } = serve([answered]);

    renderCalls("/calls?range=yesterday");
    await screen.findByText("2026-10-08 12:00:00");

    const midnight = new Date();
    midnight.setHours(0, 0, 0, 0);
    const start = new Date(midnight);
    start.setDate(start.getDate() - 1);

    expect(lastQuery(urls).get("start")).toBe(start.toISOString());
    expect(lastQuery(urls).get("end")).toBe(midnight.toISOString());
  });

  it("ends a custom range at the start of the minute after its last", async () => {
    const { urls } = serve([answered]);

    renderCalls();
    await screen.findByText("2026-10-08 12:00:00");

    fireEvent.click(
      screen.getByRole("button", { name: "Time range: Last 24 hours" }),
    );
    fireEvent.change(screen.getByLabelText("From"), {
      target: { value: "2026-10-08T12:00" },
    });
    fireEvent.change(screen.getByLabelText("To"), {
      target: { value: "2026-10-08T12:30" },
    });

    const end = new Date("2026-10-08T12:31").toISOString();
    await waitFor(() => expect(lastQuery(urls).get("end")).toBe(end));
    expect(screen.getByLabelText("To")).toHaveValue("2026-10-08T12:30");
  });

  it("polls the first page, unless searching", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });

    try {
      for (const [path, polls] of [
        ["/calls", true],
        ["/calls?search=alice", false],
      ] as const) {
        const { urls } = serve([answered]);
        const { unmount } = renderCalls(path);

        await screen.findByText("2026-10-08 12:00:00");
        const fetched = urls.length;

        await vi.advanceTimersByTimeAsync(CALLS_REFRESH_MS * 2);
        expect(urls.length > fetched).toBe(polls);

        unmount();
      }
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a call as the IMS last recorded it", async () => {
    serve(
      [ringing],
      [
        {
          ...ringing,
          sip_status: 200,
          outcome: "answered",
          ended_by: "caller",
          in_progress: false,
        },
      ],
    );

    renderCalls();
    fireEvent.click(await screen.findByText("2026-10-08 12:10:00"));

    const drawer = await screen.findByRole("dialog");
    await waitFor(() => expect(drawer).toHaveTextContent("SIP Status200"));
    expect(drawer).toHaveTextContent("Ended ByCaller");
  });

  it("edits the retention", async () => {
    const { puts } = serve([]);

    renderCalls();

    await waitFor(() =>
      expect(screen.getByText(/^Retention:/)).toHaveTextContent(
        "Retention: 90 days",
      ),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "edit call record retention" }),
    );

    const days = await screen.findByRole("textbox", { name: "Days" });
    fireEvent.change(days, { target: { value: "0" } });
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fireEvent.change(days, { target: { value: "30" } });
    expect(
      screen.getByText(/Reducing retention from 90 to 30 days/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.getByText(/^Retention:/)).toHaveTextContent(
        "Retention: 30 days",
      ),
    );
    expect(puts).toEqual([{ days: 30 }]);
  });
});
