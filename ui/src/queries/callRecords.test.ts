import { afterEach, describe, expect, it, vi } from "vitest";
import {
  listCallRecords,
  updateCallRecordRetention,
} from "@/queries/callRecords";

const stub = (result: unknown) => {
  const fetchMock = vi.fn(
    async (_url: string, _init?: RequestInit) =>
      new Response(JSON.stringify({ result }), { status: 200 }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listCallRecords", () => {
  it("maps the filter and pagination to query parameters", async () => {
    const fetchMock = stub({
      items: [],
      page: 1,
      per_page: 25,
      total_count: 0,
    });

    await listCallRecords({
      page: 1,
      perPage: 25,
      search: "+1555",
      start: "2026-10-08T00:00:00.000Z",
      outcomes: ["busy", "no_answer"],
    });

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/v1/call-records?page=1&per_page=25&search=%2B1555&start=2026-10-08T00%3A00%3A00.000Z&outcome=busy&outcome=no_answer",
    );
  });
});

describe("updateCallRecordRetention", () => {
  it("puts the days", async () => {
    const fetchMock = stub({ days: 30 });

    await expect(updateCallRecordRetention({ days: 30 })).resolves.toEqual({
      days: 30,
    });

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/call-records/retention");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: "PUT",
      body: '{"days":30}',
    });
  });
});
