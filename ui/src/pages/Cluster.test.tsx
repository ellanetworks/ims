import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Cluster from "@/pages/Cluster";
import { authFor } from "@/contexts/AuthContext";
import type { ClusterStatus } from "@/queries/cluster";
import { RoleID } from "@/queries/users";
import { clusterMember, clusterStatus } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = (status: ClusterStatus) =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/cluster": () => json(200, { result: status }),
      }),
    ),
  );

const grid = () => screen.getByRole("grid", { name: "Nodes" });

const row = (text: string) =>
  within(grid()).getByText(text).closest(".MuiDataGrid-row") as HTMLElement;

const rowCells = (text: string) =>
  within(row(text))
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

const settingRows = () =>
  Array.from(
    screen
      .getByRole("table", { name: "Cluster state" })
      .querySelectorAll(":scope > tbody > tr"),
  ).map((r) => r.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Cluster", () => {
  it("shows the cluster state and every member", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    expect(
      await screen.findByRole("heading", { level: 2, name: "Nodes (3)" }),
    ).toBeInTheDocument();
    expect(settingRows()).toEqual([
      "Cluster ID9f1e2d3c-2222-4b5a-8c7d-6e5f4a3b2c1d",
      "HealthHealthyTolerates 1 voter failure",
      "Schemav1",
    ]);
    expect(
      within(grid())
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).toEqual([
      "Node",
      "Cluster Address",
      "API Address",
      "Diameter Host",
      "Suffrage",
      "Drain",
      "Version",
      "Healthy",
      "Actions",
    ]);
    await within(grid()).findByText(
      "ims-1f0c2d3e4f50.ims.mnc001.mcc001.3gppnetwork.org",
    );
    expect(rowCells("10.0.0.11:7000").slice(0, 8)).toEqual([
      "ims-1f0c2d3e4f50LeaderThis node",
      "10.0.0.11:7000",
      "https://10.0.0.11:5020",
      "ims-1f0c2d3e4f50.ims.mnc001.mcc001.3gppnetwork.orgGenerated",
      "Voter",
      "Active",
      "v0.1.0",
      "Healthy",
    ]);
    expect(rowCells("10.0.0.12:7000").slice(0, 4)).toEqual([
      "paris-2",
      "10.0.0.12:7000",
      "https://10.0.0.12:5020",
      "ims-b.ims.mnc001.mcc001.3gppnetwork.org",
    ]);
    expect(rowCells("10.0.0.13:7000").slice(0, 8)).toEqual([
      "ims-3b2e4f5a6b72",
      "10.0.0.13:7000",
      "—",
      "ims-3b2e4f5a6b72.ims.mnc001.mcc001.3gppnetwork.orgGenerated",
      "Catching up",
      "Drained",
      "v0.1.0",
      "Unhealthy",
    ]);
  });

  it("shows the per-node view on a follower and points to the leader", async () => {
    const leader = clusterMember({
      node_id: "1c6d7b1f-3333-4d2f-8b66-2a1d3e4f5a61",
      address: "10.0.0.12:7000",
      api_address: "https://10.0.0.12:5020",
      display_name: "paris-2",
      leader: true,
      healthy: undefined,
      last_contact: undefined,
    });
    serve(
      clusterStatus({
        leader_id: leader.node_id,
        healthy: undefined,
        failure_tolerance: undefined,
        members: [
          clusterMember({ healthy: undefined, last_contact: undefined }),
          leader,
        ],
      }),
    );
    renderWithClient(<Cluster />);

    await screen.findByRole("heading", { level: 2, name: "Nodes (2)" });
    expect(settingRows()[1]).toBe(
      "HealthShown on the leader (node paris-2): https://10.0.0.12:5020",
    );
    expect(
      within(screen.getByRole("table", { name: "Cluster state" })).getByRole(
        "link",
        { name: "https://10.0.0.12:5020" },
      ),
    ).toHaveAttribute("href", "https://10.0.0.12:5020");
    expect(
      within(grid()).queryByRole("columnheader", { name: "Healthy" }),
    ).not.toBeInTheDocument();
  });

  it("says when no leader answers", async () => {
    serve(
      clusterStatus({
        leader_id: undefined,
        healthy: undefined,
        failure_tolerance: undefined,
        members: [clusterMember({ healthy: undefined })],
      }),
    );
    renderWithClient(<Cluster />);

    expect(await screen.findByText("No leader")).toBeInTheDocument();
  });

  it("explains how to enable high availability on a standalone node", async () => {
    serve(clusterStatus({ clustered: false, members: [clusterMember()] }));
    renderWithClient(<Cluster />);

    expect(
      await screen.findByText("High availability is not enabled"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("grid")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add Node" }),
    ).not.toBeInTheDocument();
  });

  it("lets an admin add a node and act on members", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    fireEvent.click(await screen.findByRole("button", { name: "Add Node" }));
    expect(
      screen.getByRole("dialog", { name: "Add a Node to the Cluster" }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    fireEvent.click(
      within(row("10.0.0.12:7000")).getByRole("button", {
        name: "Node actions",
      }),
    );
    expect(
      screen.getAllByRole("menuitem").map((item) => item.textContent),
    ).toEqual(["Rename this node", "Drain this node", "Remove from cluster"]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Drain this node" }));
    expect(
      await screen.findByRole("dialog", { name: "Drain node paris-2?" }),
    ).toBeInTheDocument();
  });

  it("opens the rename dialog with the node's display name", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    await screen.findByRole("grid", { name: "Nodes" });
    fireEvent.click(
      within(row("10.0.0.12:7000")).getByRole("button", {
        name: "Node actions",
      }),
    );
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename this node" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename node" });
    expect(
      within(dialog).getByRole("textbox", { name: "Display name" }),
    ).toHaveValue("paris-2");
  });

  it("offers to resume a drained node", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    await screen.findByRole("grid", { name: "Nodes" });
    fireEvent.click(
      within(row("10.0.0.13:7000")).getByRole("button", {
        name: "Node actions",
      }),
    );
    fireEvent.click(screen.getByRole("menuitem", { name: "Resume this node" }));
    expect(
      await screen.findByRole("dialog", {
        name: "Resume node ims-3b2e4f5a6b72?",
      }),
    ).toBeInTheDocument();
  });

  it("shows a read-only user the cluster without actions", async () => {
    serve(clusterStatus());
    renderWithClient(
      <Cluster />,
      authFor("viewer@example.com", RoleID.ReadOnly),
    );

    await screen.findByRole("grid", { name: "Nodes" });
    expect(
      screen.queryByRole("button", { name: "Add Node" }),
    ).not.toBeInTheDocument();
    expect(
      within(grid()).queryByRole("columnheader", { name: "Actions" }),
    ).not.toBeInTheDocument();
  });
});
