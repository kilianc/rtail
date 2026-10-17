# Licensing

rTail is licensed under the [Apache License 2.0](LICENSE).

rTail **v1.x** (everything up to and including `v0.2.1`, and the history on
`main` before the v2 branch) was released under the MIT licence and stays that
way. Nothing here makes an older release less permissive — if you are using v1,
the MIT terms you accepted still apply, forever.

rTail **v2** was briefly licensed under AGPL-3.0, between commits `3e8804c` and
the one that carries this file. If you took a copy in that window, that copy is
AGPL and you may keep using it on those terms; you may also simply take the
Apache-licensed version, which is strictly more permissive. Nobody is worse off
either way.

## What Apache-2.0 means for you

- **Run it, anywhere, for anything.** Your machines, your customers' machines,
  inside a product you sell. Commercially, at any scale.
- **Modify it freely**, and you owe nobody your changes.
- **Offer it as a service** if you want to, modified or not, with no obligation
  to publish anything.
- **There is a patent grant** (§3). Contributors grant you a licence to their
  patent claims covering their contributions, and it terminates only if you
  sue someone over the software — which is the protection MIT does not give.
- **The obligations are small:** keep the licence and copyright notices, state
  what you changed in files you modified, and do not use the project's
  trademarks to imply endorsement (§6 withholds those explicitly).

There is nothing to buy, no feature held back for a paid tier, and no
enterprise edition. There is no other version of rTail.

## Why this, and why it changed twice

**v1 was MIT.** `3e8804c` relicensed v2 to AGPL-3.0 for a commercial reason: v2
was going to have a hosted service behind it, and AGPL was chosen so a better
resourced company could not run the project as a managed service, contribute
nothing back, and outcompete the authors with their own code.

**There is no business.** rTail is free software, with no hosted service and
nothing to sell, so the argument AGPL was chosen to win is not being fought.
§9 of [the design proposal](docs/proposal-logging-system.md) records that path
and is marked superseded.

The licence was kept at AGPL for a while on a second argument — that copyleft
keeps improvements public, and that a tool promising your logs outlive it
should not be closable downstream. That argument does not hold:

- **The format promise is kept by the format, not the licence.** A proprietary
  fork cannot make anybody's existing Parquet unreadable. Parquet is an open
  standard, the on-disk layout is documented, and the catalog is SQLite. Your
  data survives a closed fork exactly as well as it survives rTail being
  abandoned — which is the scenario the promise was written against. Tying the
  data guarantee to the code licence conflated two independent things.
- **AGPL's cost is concrete and buys nothing here.** It is banned by policy
  inside a number of large companies — Google's published policy is the
  clearest example — which for a commercial project is a feature, because it
  routes those companies to a commercial licence. With no commercial licence to
  route them to, it is cost with no corresponding benefit.
- **Copyleft was not protecting the design either.** A permissive original
  keeps existing and keeps being forkable, and a fork only harms if it
  outcompetes and replaces the original — which happens when the forker is far
  better resourced, and which copyleft does not prevent, because anyone in that
  position reimplements rather than forks. OpenObserve and Parseable arrived at
  this same architecture independently, without needing anyone's code.
- **It discouraged the use rTail is for.** AGPL §13 binds whoever modifies the
  program and offers it to users over a network, and the natural way to run a
  log explorer is as an internal service for colleagues. A conservative reading
  says a team that patches it owes its users the source — so the licence
  discouraged exactly the modify-and-deploy-internally behaviour this project
  wants, hardest in the organisations with the log volumes to make it
  interesting.
- **The decision is one-way.** Apache code can be taken into a GPLv3 or AGPL
  project later; copyleft cannot move back. And without a CLA, once there are
  outside AGPL contributions the choice becomes permanent. Choosing the
  reversible option while it is still reversible is the whole argument.

Apache rather than MIT for the patent grant, and because §6 withholds trademark
rights explicitly rather than leaving it to be argued.

## Money

rTail takes sponsorship and runs nothing. There is no hosted rTail operated by
this project, now or later — a hosted tier creates users whose data is in it,
and no change of mind undoes that, so it is ruled out now while it costs a
sentence rather than a migration. Paid support is not taken up and not ruled
out.

Somebody else running a hosted rTail is welcome: that is free distribution for
the project and a competitor for nothing, because there is nothing to compete
for. The one reservation is the name, which Apache-2.0 §6 reserves — run the
software, do not imply it is ours.

## Contributing

Contributions are accepted under Apache-2.0, certified by a
[Developer Certificate of Origin](https://developercertificate.org/) sign-off:

    git commit -s

That line asserts you wrote the patch or have the right to submit it. It is not
a copyright assignment and it gives the project no rights over your work beyond
the licence everyone already has — which is exactly why it is a DCO and not a
CLA. A CLA exists so a project can relicense contributions later, usually to
sell them; there is nothing here to sell, so asking for one would be taking
something for no reason.

The MIT-licensed contributions in the v1 history remain under MIT. Nothing
about moving v2 to Apache-2.0 affects them, and because both MIT and Apache-2.0
are permissive, no contributor's position is narrowed by the change.
