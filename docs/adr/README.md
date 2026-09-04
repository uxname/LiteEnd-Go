# docs/adr — architecture decision records for this backend

Decisions that belong to **this repository**: its language, its layering, its protocols,
its deliberate omissions. One decision per file, with the reason it was made — a rule
without its reason is what someone "fixes" six months later.

- **Format:** copy the shape of an existing file here — Date, Status, Context, Decision,
  Alternatives, Consequences.
- **Naming:** `NNNN-short-slug.md`, four digits, monotonic, never reused. Numbers are
  local to this repo, so cite them as `backend ADR-0001` when writing outside it.
- **Superseding:** never edit a decision. Write a new ADR and set the old one's status to
  `superseded by ADR-NNNN`.
- **Scope:** if the decision spans both sides of the stack (repo topology, the API
  contract between backend and frontend, shared process), it belongs in the LiteStack
  meta-repo's `docs/adr/` instead.

When this repo is used inside LiteStack, the meta-repo's `docs/adr/README.md` holds the
full rules and the template.
