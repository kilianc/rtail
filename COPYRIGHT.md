# Licensing

rTail **v2 and later** is licensed under the
[GNU Affero General Public License, version 3](LICENSE).

rTail **v1.x** (everything up to and including `v0.2.1`, and the entire history
on `main` before the v2 branch) was released under the MIT licence and stays
that way. Nothing about this change makes an older release less permissive —
if you are using v1, the MIT terms you accepted still apply, forever.

## What AGPL means for you

- **Self-hosting is free and unrestricted.** Run it on your own machines, for
  your own logs, internally, commercially, at any scale. You owe nothing and
  you have nothing to publish.
- **Modify it freely.** Change anything you like for your own use.
- **The one obligation:** if you offer a modified rTail *to others over a
  network as a service*, you must make your modified source available to those
  users. That is the whole point of the A in AGPL, and it is the only thing it
  adds over GPL.

If that obligation is a problem for something you want to do, get in touch —
a commercial licence is available.

## Why the change

rTail v2 is a log storage and search engine, which is a product category with a
well-established pattern: a company takes the open source project, runs it as a
managed service, contributes nothing back, and outcompetes the original authors
using their own code. AGPL does not prevent competition; it requires that
anyone competing on this codebase does so with their cards face up.

Adoption cost is real and understood. The alternative considered was BSL, which
would have barred hosted offerings outright for a term of years. AGPL was
chosen because it keeps rTail genuinely open source by the OSI definition,
which BSL is not.

## Contributor note

The MIT-licensed contributions in the v1 history remain under MIT. New
contributions to the v2 branch are accepted under AGPL-3.0.

If you contributed to rTail before the v2 branch and object to your work being
carried into an AGPL codebase, open an issue and it will be removed or
rewritten. MIT permits the relicensing of derivative works, so this is a
courtesy rather than an obligation — but it is one worth extending.
