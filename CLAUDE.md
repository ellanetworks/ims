# Ella IMS - AI Coding Agent Instructions

## Code Comments

**Write zero comments.** Do not add explanatory comments, section headers, TODOs, doc comments, field comments, or inline comments to any Go, UI, or Android code you write or modify, not even for non-obvious logic.

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
