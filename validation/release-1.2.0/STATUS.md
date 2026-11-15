# v1.2.0 acceptance checkpoint

Updated: 2026-11-15. Status: local signed release candidate; NOT published.
No release tag, public release, update feed, or production installation has been created.

Candidate Source-ID:
`aef32e40795e3989385c293c057d3b3f8025ef02b50e179b75c6e522c00cf886`

## Implemented scope

Tunnel-only shared TCP/UoT transport, route receive isolation, common configuration
schema, macOS and Windows GUI, encrypted provisioning, remote tunnel assignment,
egress configuration and managed installation/rollback. Legacy Profile/Bundle
transport modes are rejected by the public 1.2.0 launch/configuration entrypoints.

## Observed local acceptance

- Fresh full engine tests, full race tests and go vet passed on macOS arm64.
- Fresh full controller tests, race tests and go vet passed on macOS arm64.
- Ten source manifest regression tests passed, including stale root/package
  manifests, source drift, duplicate/extra archive entries and CRLF equivalence.
- A previous race run failed a legacy supervisor fixture before retry readiness.
  Distinct ports are now reserved together and early supervisor errors are surfaced;
  fatal test paths cancel their children. Both affected recovery scenarios passed
  ten race repetitions each, followed by a fresh complete test/race/vet run.
  The earlier failed log remains in the local working evidence directory.
- Compiled client and egress processes passed loopback acceptance: one Session,
  six Carriers, one accepted TCP connection at each of six Relay simulators,
  three independently verified 1 MiB transfers and clean exit codes.
- Shaped single-stream aggregation ran twice with MPX_ROUTED_PERF=1, not skipped.
  The last run measured 16.546 / 29.791 / 76.865 Mbps for 1 / 2 / 6 paths;
  six-path speedup was 4.645x. This is controlled loopback, NOT WAN evidence.
- The macOS offscreen GUI harness passed configuration validation, legacy
  rejection, encrypted remote configuration, endpoint-bound cache and inlet
  selection tests, and rendered six synthetic views. No user desktop was captured.
- The macOS Universal candidate was rebuilt using the original pinned stable-local
  signing identity, not ad-hoc signing. The code-signing certificate is self-signed;
  this is NOT Apple Developer ID notarization.
- Linux amd64/arm64 client, egress and controller cross-builds passed. This does
  NOT constitute native Linux runtime acceptance.
- Linux and Windows workflow YAML parsed successfully. Corrected output paths,
  the actual aggregation test switch, PowerShell exit-code checks, source identity
  gates and native Windows installed-GUI startup checks are committed candidates.

## Provenance and signing

Root SOURCE_ID/SOURCE_SHA256SUMS and their packaged copies now share one explicit
freeze. --check-frozen verifies current reviewed source bytes, both manifest
copies and every source archive entry; release builds reject stale identities.

Original signer SHA-1: `D60F6EDC71041789131273AF4ABE708DB2CF0DD7`.
Pinned public certificate SHA-256:
`69592FC828D27711E9ABA75E843671D6E8A53B02AE684F040D88B9F1C955535F`.

The build accepts MPTCP_CODESIGN_KEYCHAIN to reference the existing login keychain
explicitly without changing HOME or global keychain search lists. No private key,
GitHub token or keychain password was exported.

## Outstanding publication gates

Native Windows engine/client tests, installer and responsive GUI startup; native
Linux suite and container persistence; actual browser UI acceptance; final signed
updater assets and whole-suite inventory/checksum verification remain required.

The user's interactive Terminal is authorized, but the CodePier project runtime
uses a separate HOME and its gh process still cannot authenticate through the
user's default keychain. An explicit HOME override was rejected by runtime policy
and was not bypassed. The candidate branch was absent on GitHub at the last check;
no remote CI run is claimed. Pushing the committed candidate from the user's
already-authorized Terminal is the remaining authentication handoff.

## Evidence

Committed compact evidence is stored beside this file. Complete working logs are
in project-local .scratch/v120-resume, .scratch/v120-resume-tests and
.scratch/v120-package-report. Earlier work is retained in .scratch/v120-validation.
All paths above are relative to the surrounding mptcp project, not this repository.
Source-ID identifies source content; CI commit identifiers are recorded separately.

## First remote CI attempt (commit 53b9c8f)

Both GitHub Actions workflows triggered on push. Native Linux Engine tests,
controller tests, race/vet, cross-build and compiled-process aggregation passed.
The browser step failed because Playwright 1.56 did not support the then-default
Ubuntu 26.04 image; Linux runner has been pinned to Ubuntu 24.04.

The Windows build failed compiling pre-existing Unix-only Getrusage measurement
fixtures, not the product transport. Optional performance measurements now use
platform-specific helpers; the full multipath test executable cross-compiles
for Windows while runtime assertions remain enabled. macOS engine full test,
race and vet were rerun after the change and passed. Native Windows/Ubuntu
re-validation is still required before release.
