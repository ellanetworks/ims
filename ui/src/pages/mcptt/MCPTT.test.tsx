import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import MCPTTGroupCalls from "@/pages/mcptt/GroupCalls";
import MCPTTGroups from "@/pages/mcptt/Groups";
import MCPTTSettings from "@/pages/mcptt/Settings";
import MCPTTUsers from "@/pages/mcptt/Users";
import { alice, bob, fire, mcpttRoutes, mcpttStatus } from "@/test/mcptt";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = (...args: Parameters<typeof mcpttRoutes>) => {
  const fetch = vi.fn(stubApi(mcpttRoutes(...args)));
  vi.stubGlobal("fetch", fetch);
  return fetch;
};

// requests are the bodies of the requests of method to path.
const requests = (
  fetch: ReturnType<typeof serve>,
  method: string,
  path: string,
) =>
  fetch.mock.calls
    .filter(
      ([input, init]) =>
        init?.method === method &&
        new URL(String(input), "http://ims.test").pathname === path,
    )
    .map(([, init]) => JSON.parse(String(init?.body)));

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("MCPTT pages", () => {
  it("tell how to turn MCPTT on when the IMS runs none", async () => {
    serve({}, false);
    renderWithClient(<MCPTTUsers />);

    expect(await screen.findByText("Push-to-talk is off")).toBeInTheDocument();
    expect(screen.getByText(/mcptt:/)).toBeInTheDocument();
  });

  it("list the users, their groups and phones", async () => {
    serve();
    renderWithClient(<MCPTTUsers />);

    const grid = await screen.findByRole("grid", { name: "Users" });
    await within(grid).findByText("Alice");

    const rows = within(grid).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("alice")).toBeInTheDocument();
    expect(within(rows[0]).getByText("Fire")).toBeInTheDocument();
    await within(rows[0]).findByText("1 registered");
    // bob has no alias: his short MCPTT ID is his name.
    expect(within(rows[1]).getByText("bob")).toBeInTheDocument();
    expect(within(rows[1]).getByText("None")).toBeInTheDocument();
  });

  it("add a user of a name and a number", async () => {
    const fetch = serve({
      "/api/v1/mcptt/users": (_url, init) =>
        init?.method === "POST"
          ? json(201, { result: JSON.parse(String(init.body)) })
          : json(200, {
              result: {
                items: [alice, bob],
                page: 1,
                per_page: 100,
                total_count: 2,
              },
            }),
    });
    renderWithClient(<MCPTTUsers />);

    await screen.findByText("Alice");
    fireEvent.click(await screen.findByRole("button", { name: "Add User" }));

    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Name" }), {
      target: { value: "Carol" },
    });
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "MCPTT ID" }),
      { target: { value: "carol" } },
    );
    expect(
      await within(dialog).findByText("sip:carol@mcptt.example.com"),
    ).toBeInTheDocument();

    fireEvent.change(
      within(dialog).getByRole("combobox", { name: "IMS Identities" }),
      {
        target: { value: "15550000003" },
      },
    );
    expect(
      within(dialog).getByText("Adds tel:+15550000003"),
    ).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(requests(fetch, "POST", "/api/v1/mcptt/users")).toEqual([
        {
          mcptt_id: "sip:carol@mcptt.example.com",
          alias: "Carol",
          impus: ["tel:+15550000003"],
          implicit_affiliations: [],
        },
      ]),
    );
  });

  it("make a member always affiliated from its group", async () => {
    const memberPath = `/api/v1/mcptt/groups/${encodeURIComponent(fire.group_id)}/members/${encodeURIComponent(bob.mcptt_id)}`;
    const fetch = serve({ [memberPath]: () => json(200, { result: fire }) });
    renderWithClient(<MCPTTGroups />);

    fireEvent.click(await screen.findByText("Fire"));

    const drawer = await screen.findByRole("presentation");
    expect(within(drawer).getByText("Members (2)")).toBeInTheDocument();
    await within(drawer).findByText("Affiliated");

    fireEvent.click(
      within(drawer).getByRole("switch", { name: "bob always affiliated" }),
    );

    await waitFor(() =>
      expect(requests(fetch, "PUT", memberPath)).toEqual([
        { mcptt_id: bob.mcptt_id, recvonly: false, implicit_affiliation: true },
      ]),
    );
  });

  it("show the group calls and who talks", async () => {
    serve();
    renderWithClient(<MCPTTGroupCalls />);

    const grid = await screen.findByRole("grid", { name: "Group calls" });
    await within(grid).findByText("Fire");
    expect(within(grid).getByText(/^Alice · /)).toBeInTheDocument();
    expect(within(grid).getByText("1 of 2")).toBeInTheDocument();
  });

  it("show the server name to enter in the HSS", async () => {
    serve();
    renderWithClient(<MCPTTSettings />);

    const table = await screen.findByRole("table", {
      name: "This MCPTT server",
    });
    expect(table).toHaveTextContent(mcpttStatus.name);
    expect(
      await screen.findByRole("table", { name: "Service" }),
    ).toHaveTextContent("30 s (default)");
  });

  it("edit each setting in a dialog of its own", async () => {
    const fetch = serve({
      "/api/v1/mcptt/service-configuration": (_url, init) =>
        json(200, {
          result:
            init?.method === "PUT"
              ? JSON.parse(String(init.body))
              : { domain: "mcptt.example.com" },
        }),
    });
    renderWithClient(<MCPTTSettings />);

    await screen.findByText("30 s (default)");
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Talk Time Limit" }),
    );

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getAllByRole("textbox")).toHaveLength(1);
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Talk Time Limit" }),
      { target: { value: "15" } },
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(
        requests(fetch, "PUT", "/api/v1/mcptt/service-configuration"),
      ).toEqual([
        { domain: "mcptt.example.com", transmit_time_limit_ms: 15000 },
      ]),
    );
  });
});
