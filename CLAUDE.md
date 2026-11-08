# Ella IMS - AI Coding Agent Instructions

## Code Comments

**Write zero comments.** Do not add explanatory comments, section headers, TODOs, doc comments, field comments, or inline comments to any Go, UI, Android, YAML (Kubernetes manifests, kustomizations, workflows) or shell code you write or modify, not even for non-obvious logic.

The only allowed comments are:

- **Spec citations**, written bare, one per line, directly above a function, method, type, or const/var declaration. No prose, no trailing period:

  ```go
  // TS 24.229 clause 5.4.1.2.1
  // RFC 3261 section 17.1.1.2
  // GSMA IR.92 section 2.4.1
  // OpenID Connect Core 1.0 section 3.1.3.7
  ```

  3GPP specs use `clause`. RFCs, GSMA, ITU-T and OpenID documents use `section`. Never put a citation inside a function body or on a struct field. Only cite a clause that you have confirmed in the spec (specs are in `../3gpp`). Never guess a clause number.
- **Comments that tooling requires**: `//go:build`, `//go:embed`, `//go:generate`, `//nolint:<linter>` (with no trailing reason), `// eslint-disable…`, `// oxlint-disable…`, `// @ts-expect-error`.

**Do not touch existing citations** unless your change makes one wrong. In that case, fix it or delete it.

## Docs

The docs follow Diátaxis (https://diataxis.fr). Each page is one of four types, and holds only that type's content:

- **Tutorials** (`docs/tutorials/`): a lesson that takes a beginner to a working result. Concrete steps that always work, in a fixed order, with the result shown at each step. No choices, no alternatives, no explanation beyond what the next step needs.
- **How-to guides** (`docs/how_to/`): steps to reach one goal, for a user who already knows the product. Imperative steps only. No "why", no background, no internals.
- **Reference** (`docs/reference/`): a dry description of the product, e.g. API endpoints, config keys, ports, metrics. Tables and short statements of fact. No instructions, no rationale, no implementation details.
- **Explanation** (`docs/explanation/`): the "why": design, trade-offs, context. The only place for rationale.
