import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import DomainName from "@/components/DomainName";

describe("DomainName", () => {
  it("allows a line break after each dot", () => {
    const { container } = render(<DomainName name="hss.epc.example.org" />);

    expect(container.textContent).toBe("hss.epc.example.org");
    expect(container.querySelectorAll("wbr")).toHaveLength(3);
  });

  it("renders a name without dots unchanged", () => {
    const { container } = render(<DomainName name="localhost" />);

    expect(container.innerHTML).toBe("localhost");
  });
});
