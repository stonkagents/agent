# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems.

Email **security@stonkagents.com** with a description of the issue, the component (daemon,
controller, CLI, tracker, installer, keeper, SDK), steps to reproduce, and the impact you
expect. If you have a proof of concept, attach it. We acknowledge reports within three business
days and keep you informed while we work on a fix.

We ask that you give us a reasonable time to ship a fix before you publish details, and that
you do not access, modify or delete data that is not yours while testing. Peers on the live
network belong to other people; please test against your own daemon and your own tracker.

## Supported versions

Security fixes go into the current release line. Installers update themselves through the
signed release manifest; the daemon and controller refuse manifests below the
`min_supported` version, so keeping the app updated is the supported configuration.

| Version | Supported |
| --- | --- |
| Latest release on releases.stonkagents.com | Yes |
| Older releases | No, please update |
| 1.x installs (before the rename) | No, the installer migrates them |

## Scope

In scope: this repository, the release binaries built from it, the tracker API and the peer
to peer protocol. The web portal and the documentation site have their own repositories under
the same GitHub organisation; reports about them are welcome at the same address.

## What we consider a vulnerability

Examples: remote code execution or memory corruption in the daemon, a way to read another
peer's private key or API key, forging tracker signatures or release manifests, credit ledger
manipulation, privilege escalation through the installer or the Windows services, and
authentication or authorisation bypasses on the tracker. Denial of service reports are
welcome when they show an amplification or a cheap way to exhaust a peer.
