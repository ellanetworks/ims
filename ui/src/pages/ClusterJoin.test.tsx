import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import ClusterJoin from "@/pages/ClusterJoin";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = ({
  initialized = false,
  clustered = true,
  join = () => json(200, { result: { message: "Joined the cluster" } }),
  bodies = [],
  copies = 0,
}: {
  initialized?: boolean;
  clustered?: boolean;
  join?: () => Response;
  bodies?: unknown[];
  copies?: number;
} = {}) => {
  let joined = false;
  let polls = 0;
  const fetch = vi.fn(
    stubApi({
      "/api/v1/status": () =>
        json(200, {
          result: {
            version: "v0.0.1",
            revision: "",
            initialized: initialized || (joined && polls++ >= copies),
            clustered,
          },
        }),
      "/api/v1/cluster/join": (_url, init) => {
        bodies.push(JSON.parse(String(init?.body)));
        const res = join();
        joined = res.ok;
        return res;
      },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
};

const renderJoin = () =>
  renderWithClient(
    <MemoryRouter initialEntries={["/initialize/join"]}>
      <Routes>
        <Route path="/initialize/join" element={<ClusterJoin />} />
        <Route path="/initialize" element={<p>initialize page</p>} />
        <Route path="/login" element={<p>login page</p>} />
      </Routes>
    </MemoryRouter>,
  );

const tokenField = () => screen.getByRole("textbox", { name: /Join token/ });

const paste = (text: string) =>
  fireEvent.paste(tokenField(), {
    clipboardData: { getData: () => text },
  });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ClusterJoin", () => {
  it("sends an initialized node to the login page", async () => {
    serve({ initialized: true });
    renderJoin();

    expect(await screen.findByText("login page")).toBeInTheDocument();
  });

  it("explains that a node without a cluster address cannot join", async () => {
    serve({ clustered: false });
    renderJoin();

    expect(
      await screen.findByText(/This node has no cluster address/),
    ).toBeInTheDocument();
    expect(tokenField()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Join" })).toBeDisabled();
  });

  it("joins the cluster with the pasted token", async () => {
    const bodies: unknown[] = [];
    serve({ bodies });
    renderJoin();

    await screen.findByRole("heading", { name: "Join a cluster" });
    const join = screen.getByRole("button", { name: "Join" });
    expect(join).toBeDisabled();

    paste("  join-token-abcd \n");
    expect(screen.getByText("Token pasted · ends in abcd")).toBeInTheDocument();
    fireEvent.click(join);

    expect(await screen.findByText("login page")).toBeInTheDocument();
    expect(bodies).toEqual([{ token: "join-token-abcd" }]);
    expect(screen.getByText(/Joined the cluster/)).toBeInTheDocument();
  });

  it("waits for the cluster's data before the login page", async () => {
    serve({ copies: 1 });
    renderJoin();

    await screen.findByRole("heading", { name: "Join a cluster" });
    paste("join-token-abcd");
    fireEvent.click(screen.getByRole("button", { name: "Join" }));

    expect(await screen.findByText(/copying its data/)).toBeInTheDocument();
    expect(screen.queryByText("login page")).not.toBeInTheDocument();
    expect(
      await screen.findByText("login page", {}, { timeout: 3000 }),
    ).toBeInTheDocument();
  });

  it("lets the operator clear a pasted token", async () => {
    serve();
    renderJoin();

    await screen.findByRole("heading", { name: "Join a cluster" });
    paste("join-token-abcd");
    fireEvent.click(screen.getByRole("button", { name: "Clear join token" }));

    expect(tokenField()).toHaveValue("");
    expect(screen.getByRole("button", { name: "Join" })).toBeDisabled();
  });

  it("shows why the join failed and stays on the page", async () => {
    serve({
      join: () =>
        json(403, {
          error: "This node has a user: only a node with no user can join",
        }),
    });
    renderJoin();

    await screen.findByRole("heading", { name: "Join a cluster" });
    paste("join-token-abcd");
    fireEvent.click(screen.getByRole("button", { name: "Join" }));

    expect(
      await screen.findByText(/only a node with no user can join/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Join" })).toBeEnabled();
  });

  it("goes back to the first-user setup", async () => {
    serve();
    renderJoin();

    fireEvent.click(await screen.findByRole("button", { name: "Back" }));

    expect(await screen.findByText("initialize page")).toBeInTheDocument();
  });
});
