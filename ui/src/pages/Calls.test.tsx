import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Calls from "@/pages/Calls";
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

const serve = (items: CallRecord[]) => {
  const urls: string[] = [];
  const puts: unknown[] = [];
  let days = 90;

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
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

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Calls", () => {
  it("lists the calls", async () => {
    serve([ringing, busy, answered]);

    renderWithClient(<Calls />);

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

  it("filters by outcome", async () => {
    const { urls } = serve([busy, answered]);

    renderWithClient(<Calls />);
    await screen.findByText("2026-10-08 12:00:00");

    fireEvent.mouseDown(screen.getByRole("combobox", { name: "Outcome" }));
    fireEvent.click(await screen.findByRole("option", { name: "busy" }));
    fireEvent.click(await screen.findByRole("option", { name: "no answer" }));

    await waitFor(() =>
      expect(urls.at(-1)).toBe(
        "?page=1&per_page=25&outcome=busy&outcome=no_answer",
      ),
    );
    await waitFor(() =>
      expect(screen.queryByText("2026-10-08 12:00:00")).not.toBeInTheDocument(),
    );
  });

  it("shows the details of a call", async () => {
    serve([answered]);

    renderWithClient(<Calls />);
    fireEvent.click(await screen.findByText("2026-10-08 12:00:00"));

    const drawer = await screen.findByRole("dialog");
    expect(
      within(drawer).getByRole("heading", {
        name: "+15551230001 → +15551230002",
      }),
    ).toBeInTheDocument();
    expect(drawer).toHaveTextContent("SIP Status200");
    expect(drawer).toHaveTextContent("Ended ByCaller");
    expect(drawer).toHaveTextContent(`ICID${answered.icid}`);
    expect(
      within(drawer).getByRole("button", { name: "Copy ICID" }),
    ).toBeInTheDocument();
  });

  it("edits the retention", async () => {
    const { puts } = serve([]);

    renderWithClient(<Calls />);

    fireEvent.click(
      await screen.findByRole("button", { name: "Kept 90 days" }),
    );

    const days = await screen.findByRole("textbox", { name: "Days" });
    fireEvent.change(days, { target: { value: "0" } });
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fireEvent.change(days, { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    expect(
      await screen.findByRole("button", { name: "Kept 30 days" }),
    ).toBeInTheDocument();
    expect(puts).toEqual([{ days: 30 }]);
  });
});
