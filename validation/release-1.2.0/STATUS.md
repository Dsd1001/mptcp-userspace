# v1.2.0 acceptance checkpoint

Status: release candidate source; NOT a published production release.

Implemented: tunnel-only shared TCP/UoT transport, route receive isolation,
common configuration schema, macOS and Windows GUI, encrypted provisioning,
remote tunnel assignment, egress configuration and managed install/rollback.

Observed local gates: full engine/controller tests, race and vet passed;
Windows test executable cross-compiles; macOS offscreen UI contract/rendering
harness passes. FIN-before-DATA route budget regression also passes with race.

Required before formal publication: native Windows tests, installer and GUI
startup; native Linux suite and container persistence; browser UI acceptance;
final matched-source binaries, signatures, source/checksum manifests and
verified release assets. Controlled and loopback measurements are not WAN
performance evidence. No production machine or installed application changed.

Detailed working logs are in the project-local .scratch/v120-validation.
CI acceptance artifacts identify their source commit and Source-ID separately.
