import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import DeploymentIdentity from "@/components/DeploymentIdentity";
import { json, renderWithClient, stubApi } from "@/test/render";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DeploymentIdentity", () => {
  it("shows the version", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/status": () =>
            json(200, { result: { version: "v0.0.1", revision: "85faf74" } }),
        }),
      ),
    );

    renderWithClient(<DeploymentIdentity />);

    expect(await screen.findByText("v0.0.1")).toBeInTheDocument();
    expect(screen.queryByText("85faf74")).not.toBeInTheDocument();
  });

  const serve = (displayName: string, diameterLabel = "ims-013c0a7ca824") =>
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/status": () =>
            json(200, {
              result: {
                version: "v0.0.1",
                revision: "",
                initialized: true,
                clustered: true,
                node_id: "01a1ac75-e354-762b-b38f-013c0a7ca824",
                display_name: displayName,
                diameter_label: diameterLabel,
                diameter_host: `${diameterLabel}.ims.mnc001.mcc001.3gppnetwork.org`,
                label_generated: diameterLabel === "ims-013c0a7ca824",
              },
            }),
        }),
      ),
    );

  it("shows the node's Diameter host label without a display name", async () => {
    serve("", "paris-a");
    renderWithClient(<DeploymentIdentity />);

    const identity = await screen.findByText("v0.0.1 · Node paris-a");
    expect(identity).toHaveAttribute(
      "title",
      "paris-a.ims.mnc001.mcc001.3gppnetwork.org",
    );
  });

  it("shows the node's short id without a display name or host label", async () => {
    serve("", "");
    renderWithClient(<DeploymentIdentity />);

    expect(
      await screen.findByText("v0.0.1 · Node 013c0a7ca824"),
    ).toBeInTheDocument();
  });

  it("shows the node's display name", async () => {
    serve("paris-1");
    renderWithClient(<DeploymentIdentity />);

    expect(
      await screen.findByText("v0.0.1 · Node paris-1"),
    ).toBeInTheDocument();
  });

  it("shows nothing until the status loads", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );

    const { container } = renderWithClient(<DeploymentIdentity />);

    expect(container).toBeEmptyDOMElement();
  });
});
