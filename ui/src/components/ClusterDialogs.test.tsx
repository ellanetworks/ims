import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import {
  AddNodeDialog,
  DrainNodeDialog,
  RemoveNodeDialog,
  RenameNodeDialog,
  ResumeNodeDialog,
} from "@/components/ClusterDialogs";
import { clusterMember } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

type Call = { method: string; path: string; search: string; body?: unknown };

const MEMBER = clusterMember();
const MEMBER_PATH = `/api/v1/cluster/members/${MEMBER.node_id}`;

const serve = (respond: (url: URL) => Response = () => ok()) => {
  const calls: Call[] = [];
  const record = (url: URL, init?: RequestInit) => {
    calls.push({
      method: init?.method ?? "GET",
      path: url.pathname,
      search: url.search,
      ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    return respond(url);
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/cluster/tokens": record,
        [MEMBER_PATH]: record,
        [`${MEMBER_PATH}/drain`]: record,
        [`${MEMBER_PATH}/resume`]: record,
      }),
    ),
  );
  return calls;
};

const ok = () => json(200, { result: { message: "ok" } });

const dialog = () => screen.getByRole("dialog");
const button = (name: RegExp | string) =>
  within(dialog()).getByRole("button", { name });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AddNodeDialog", () => {
  it("names its dialog and asks only for a token lifetime", async () => {
    serve();
    renderWithClient(<AddNodeDialog onClose={() => {}} />);

    expect(dialog()).toHaveAccessibleName("Add a Node to the Cluster");
    expect(
      await screen.findByRole("combobox", { name: /Token lifetime/ }),
    ).toHaveTextContent("30 minutes");
    expect(button(/Create Token/)).toBeEnabled();
  });

  it("creates a token with the chosen lifetime and shows it once", async () => {
    const calls = serve(() =>
      json(201, {
        result: {
          token: "join-abc123",
          expires_at: "2099-01-01T00:00:00Z",
        },
      }),
    );
    const onClose = vi.fn();
    renderWithClient(<AddNodeDialog onClose={onClose} />);

    fireEvent.mouseDown(screen.getByRole("combobox", { name: /lifetime/ }));
    fireEvent.click(await screen.findByRole("option", { name: "1 hour" }));
    fireEvent.click(button(/Create Token/));

    expect(await screen.findByTestId("join-token")).toHaveTextContent(
      "join-abc123",
    );
    expect(calls).toEqual([
      {
        method: "POST",
        path: "/api/v1/cluster/tokens",
        search: "",
        body: { ttl_seconds: 3600 },
      },
    ]);
    expect(screen.getByText(/shown only once/)).toBeInTheDocument();
    expect(
      screen.getByText("Join an existing cluster instead"),
    ).toBeInTheDocument();
    expect(button("Copy join token")).toBeInTheDocument();
    expect(
      within(dialog()).queryByRole("button", { name: /Create Token/ }),
    ).not.toBeInTheDocument();

    fireEvent.click(button(/^Close$/));
    expect(onClose).toHaveBeenCalled();
  });

  it("shows why the token could not be created", async () => {
    serve(() =>
      json(409, {
        error: "this node has no cluster address",
      }),
    );
    renderWithClient(<AddNodeDialog onClose={() => {}} />);

    fireEvent.click(button(/Create Token/));

    expect(
      await screen.findByText(/this node has no cluster address/),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("join-token")).not.toBeInTheDocument();
  });
});

describe("DrainNodeDialog", () => {
  it("drains the node and closes", async () => {
    const calls = serve();
    const onClose = vi.fn();
    renderWithClient(
      <DrainNodeDialog
        member={MEMBER}
        isLastActive={false}
        isSelf={false}
        onClose={onClose}
      />,
    );

    expect(dialog()).toHaveAccessibleName("Drain node ims-1f0c2d3e4f50?");
    expect(screen.getByText(MEMBER.node_id)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.click(button("Drain"));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls).toEqual([
      { method: "POST", path: `${MEMBER_PATH}/drain`, search: "" },
    ]);
  });

  it("warns about the leader, this node and the last active node", () => {
    serve();
    renderWithClient(
      <DrainNodeDialog
        member={{ ...MEMBER, leader: true }}
        isLastActive
        isSelf
        onClose={() => {}}
      />,
    );

    expect(
      screen.getByText(/no node left to register through/),
    ).toBeInTheDocument();
    expect(screen.getByText(/This node is the leader/)).toBeInTheDocument();
    expect(
      screen.getByText(/This is the node serving this page/),
    ).toBeInTheDocument();
  });

  it("keeps the dialog open with the error when the drain is refused", async () => {
    serve(() =>
      json(409, { error: "the only member of the cluster cannot be drained" }),
    );
    const onClose = vi.fn();
    renderWithClient(
      <DrainNodeDialog
        member={MEMBER}
        isLastActive
        isSelf={false}
        onClose={onClose}
      />,
    );

    fireEvent.click(button("Drain"));

    expect(
      await screen.findByText(/the only member of the cluster/),
    ).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("ResumeNodeDialog", () => {
  it("resumes the node", async () => {
    const calls = serve();
    const onClose = vi.fn();
    renderWithClient(
      <ResumeNodeDialog
        member={{ ...MEMBER, drain_state: "drained" }}
        onClose={onClose}
      />,
    );

    expect(dialog()).toHaveAccessibleName("Resume node ims-1f0c2d3e4f50?");
    fireEvent.click(button("Resume"));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls).toEqual([
      { method: "POST", path: `${MEMBER_PATH}/resume`, search: "" },
    ]);
  });
});

describe("RemoveNodeDialog", () => {
  it("removes a drained node without force", async () => {
    const calls = serve();
    const onClose = vi.fn();
    renderWithClient(
      <RemoveNodeDialog
        member={{ ...MEMBER, drain_state: "drained" }}
        isSelf={false}
        onClose={onClose}
      />,
    );

    expect(dialog()).toHaveAccessibleName("Remove node ims-1f0c2d3e4f50?");
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();

    fireEvent.click(button("Remove"));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls).toEqual([
      { method: "DELETE", path: MEMBER_PATH, search: "" },
    ]);
  });

  it("requires force to remove a node that is not drained", async () => {
    const calls = serve();
    renderWithClient(
      <RemoveNodeDialog member={MEMBER} isSelf={false} onClose={() => {}} />,
    );

    const force = screen.getByRole("checkbox", { name: /Force remove/ });
    expect(force).not.toBeChecked();
    expect(button("Remove")).toBeDisabled();

    fireEvent.click(force);
    fireEvent.click(button("Remove"));

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "DELETE", path: MEMBER_PATH, search: "?force=true" },
      ]),
    );
  });

  it("links to the leader when this node is not the leader", async () => {
    serve(() =>
      json(421, {
        error:
          "This node is not the cluster leader: send the request to https://10.0.0.12:5020",
        leader: "https://10.0.0.12:5020",
      }),
    );
    const onClose = vi.fn();
    renderWithClient(
      <RemoveNodeDialog
        member={{ ...MEMBER, drain_state: "drained" }}
        isSelf={false}
        onClose={onClose}
      />,
    );

    fireEvent.click(button("Remove"));

    expect(
      await screen.findByText(/This node is not the cluster leader/),
    ).toBeInTheDocument();
    expect(
      within(dialog()).getByRole("link", { name: "https://10.0.0.12:5020" }),
    ).toHaveAttribute("href", "https://10.0.0.12:5020");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows no leader link for other errors", async () => {
    serve(() => json(409, { error: "the member is not drained" }));
    renderWithClient(
      <RemoveNodeDialog
        member={{ ...MEMBER, drain_state: "drained" }}
        isSelf={false}
        onClose={() => {}}
      />,
    );

    fireEvent.click(button("Remove"));

    expect(
      await screen.findByText(/the member is not drained/),
    ).toBeInTheDocument();
    expect(within(dialog()).queryByRole("link")).not.toBeInTheDocument();
  });

  it("defaults to force for an unreachable node", () => {
    serve();
    renderWithClient(
      <RemoveNodeDialog
        member={{ ...MEMBER, healthy: false }}
        isSelf
        onClose={() => {}}
      />,
    );

    expect(
      screen.getByRole("checkbox", { name: /Force remove/ }),
    ).toBeChecked();
    expect(screen.getByText(/unreachable/)).toBeInTheDocument();
    expect(
      screen.getByText(/This is the node serving this page/),
    ).toBeInTheDocument();
    expect(button("Remove")).toBeEnabled();
  });
});

describe("RenameNodeDialog", () => {
  const name = () => screen.getByRole("textbox", { name: "Display name" });

  it("starts from the current display name", () => {
    serve();
    renderWithClient(<RenameNodeDialog member={MEMBER} onClose={() => {}} />);

    expect(dialog()).toHaveAccessibleName("Rename node");
    expect(name()).toHaveValue("");
    expect(
      screen.getByText(
        "Leave empty to show the Diameter host label ims-1f0c2d3e4f50 instead (max 63 characters).",
      ),
    ).toBeInTheDocument();
    expect(button("Save")).toBeDisabled();
  });

  it("sets the display name", async () => {
    const calls = serve();
    const onClose = vi.fn();
    renderWithClient(<RenameNodeDialog member={MEMBER} onClose={onClose} />);

    fireEvent.change(name(), { target: { value: " Paris 1 " } });
    fireEvent.click(button("Save"));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls).toEqual([
      {
        method: "PUT",
        path: MEMBER_PATH,
        search: "",
        body: { display_name: "Paris 1" },
      },
    ]);
  });

  it("clears the display name", async () => {
    const calls = serve();
    const onClose = vi.fn();
    renderWithClient(
      <RenameNodeDialog
        member={{ ...MEMBER, display_name: "paris-1" }}
        onClose={onClose}
      />,
    );

    expect(name()).toHaveValue("paris-1");
    fireEvent.change(name(), { target: { value: "" } });
    fireEvent.click(button("Save"));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls).toEqual([
      {
        method: "PUT",
        path: MEMBER_PATH,
        search: "",
        body: { display_name: "" },
      },
    ]);
  });
});
