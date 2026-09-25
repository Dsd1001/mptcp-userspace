# Adaptive windows in 0.9.4 / MPX/3 Rev5

0.9.4 retains the 0.9.3 warm-seed contention guard unchanged. Weighted does not change stream-window growth, shared credit, consumption accounting or any resource limit.

The per-stream 16 KiB bootstrap, demand-driven growth and recent single-flow warm seed remain. Idle reduces the future window target but never retracts an advertised absolute limit. Maximum per-stream span remains 16 MiB.

The shared-credit model introduced in Revision 2 remains, while Rev4 scales its bounds. Global admission is based on actual committed/consumed DATA offsets rather than outstanding WINDOW entitlements. See REV2-SHARED-CREDIT.md. Per-stream grants may sum above session capacity without preallocating pages; the independently enforced 128 MiB session MAX_DATA and sliding 32/96 MiB actual-use pools are the global flow-control bounds. Physical receive pages remain an independent 128 MiB hard cap.

Auto / Aggregate / Protect / Weighted select the TCP DATA path policy. This release does not globally replace Aggregate with an experimental linger scheduler, shrink RTO to 150ms, pin streams to carriers or enlarge operating-system socket buffers.

Diagnostics distinguish stream WINDOW wait, session MAX_DATA wait, bootstrap/growth wait, pending byte/frame wait and writer turn. These counters are not all equivalent to congestion or physical memory pressure.
