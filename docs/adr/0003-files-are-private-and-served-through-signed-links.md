# ADR 0003: Uploaded files are private by default, served through signed links

- **Date:** 2026-09-13
- **Status:** accepted (supersedes the "permanent public URL" half of [ADR-0002](./0002-object-storage-and-trusted-client-ip.md))

## Context

[ADR-0002](./0002-object-storage-and-trusted-client-ip.md) moved uploads into an
S3-compatible store so any copy of the backend can serve a file any other copy wrote. It
made one further choice along the way, and that is the one revisited here: the URL kept in
the database was the object's **permanent public URL**, and the bucket was open for
anonymous reads. Everything that followed came from that decision:

- **A file link is a permanent public link.** It is handed to a browser, ends up in
  server-rendered HTML, in a chat message, in a bookmark, in a proxy cache — and it keeps
  working forever. Nothing can be revoked, because nothing was ever granted.
- **The only protection is the key being unguessable** — a date prefix plus a UUID. That
  is a real protection against guessing and none at all against sharing.
- **Uploading is authenticated, downloading is not.** `POST /upload` sits behind
  `requireAuth`; the resulting object is readable by the entire internet. For an avatar on
  a public profile page that is the correct trade. For a scanned document, an invoice, a
  private photo — the same template, one `git clone` later — it is a data leak that
  nothing in the code, the config or the tests would flag.

A template's default is what most derived products will ship, so the default has to be the
safe one, and the unsafe one has to be a sentence the operator writes on purpose.

## Decision

**1. `FILE_VISIBILITY` decides who may read an uploaded file, and its default is
`private`.**

- `private` — the bucket refuses anonymous readers, and the API hands out a **signed**
  link (S3 SigV4 presigned GET) that stops working after `FILE_LINK_TTL_MINUTES`
  (default 15, capped at the 7-day signature limit). A URL that leaks stops being useful
  in minutes.
- `public` — the bucket is world-readable and links are permanent, exactly the ADR-0002
  behaviour. Simpler, cacheable, and appropriate when the files genuinely are public.

**2. What the database stores is the permanent reference, never a signature.**
`S3_PUBLIC_BASE_URL + "/" + key` is a name for the object; the signature is a grant, and a
grant that was stored would expire and take the avatar with it. The resolver signs a fresh
link on every read (`resolver.avatarLink`) and normalises whatever the client hands back
into that permanent form (`resolver.storedAvatar`), so the browser can post the very link
it just received from `POST /upload` without knowing any of this.

**3. `POST /upload` answers with both.** `path` is the ready-to-use link (signed unless
files are public), `key` is the object's permanent name. A client that only wants to show
the image uses `path`; a client that wants to keep a reference has `key`.

**4. Signing uses a second S3 client bound to the PUBLIC address.** A SigV4 signature
covers the host and the path it was made for, so a link signed against the internal
endpoint (`http://garage:3900`) is refused in every browser. Hence the second client, the
path-style addressing, and the boot-time rule that in private mode `S3_PUBLIC_BASE_URL`
must be `<public S3 API address>/<bucket>` — the exact prefix a signed link is built from.
A proxy in front of it must pass `/<bucket>/*` through **byte for byte**: rewriting the
path or the `Host` header invalidates every signature (see `scale/Caddyfile`).

**5. Everything that opens the door is one setting away from the code.** The storage init
step in both compose files opens or closes the bucket according to the same variable, so a
stack brought up with `FILE_VISIBILITY=public` has a public bucket and one brought up
private does not. `scripts/doctor.sh` reports which mode a machine is configured for, and
warns on `public`.

## Alternatives

- **Serve files back through the app** (`GET /uploads/*` with an auth check). It works for
  any storage, but it puts every byte of every download through the copies — the exact
  coupling ADR-0002 removed — and needs streaming, range requests and caching to be
  rebuilt in application code. Rejected: the storage already does all of that, correctly.
- **Keep the public bucket and rely on unguessable keys.** That is the status quo this ADR
  replaces. It is fine for public files and indefensible as a default for everything else.
- **Store the signed link.** Simplest to write, and wrong: the value in the database dies
  on a schedule, and the first person to notice is a user staring at a broken image.
- **Per-file visibility** (this object public, that one private). A real product feature,
  and a real schema plus a policy layer. Out of scope for a template that has one file
  type and one owner; the variable is per deployment on purpose.

## Consequences

- **Private is the default, so the default deployment cannot leak a file by URL.** A link
  that escapes expires; the bucket answers an unsigned request with 403. That is pinned by
  a door test (`TestFilesArePrivate_UnsignedRequestIsRefused`) and by scenario 1 of
  `scripts/scale-check.sh`, both of which go red if a future change opens the bucket or
  drops the signing.
- **Links are not cacheable across reads, and that is the price.** Every profile read
  produces a new URL for the same bytes, so a browser cache keyed on the URL misses. The
  bytes are still cached by the storage's own headers within a link's lifetime; a product
  that needs long-lived public caching should say so with `FILE_VISIBILITY=public`.
- **Server-side rendering of a private file link is short-lived by construction.** An HTML
  page cached for longer than `FILE_LINK_TTL_MINUTES` will carry dead image URLs. Keep the
  lifetime above the page cache, or render such images client-side.
- **The signing key is the deployment's S3 credential.** Anything that can sign can grant
  access to any object in the bucket. That was already true of the app's write access; it
  now also grants reads, so the credential's blast radius is the whole bucket.
- **Switching mode on a running stack is two steps, not one.** The variable changes what
  the app hands out; `docker compose up -d` re-runs the storage init that opens or closes
  the bucket. Change one without the other and the links and the bucket disagree —
  `scripts/doctor.sh` says which mode the config claims, and the door test says what the
  storage actually does.
- **One more thing a derived product can get wrong in its proxy.** A path rewrite in front
  of the bucket used to be harmless; now it invalidates signatures. The failure is loud
  (`SignatureDoesNotMatch` on every file) and documented next to the Caddy config that
  gets it right.
