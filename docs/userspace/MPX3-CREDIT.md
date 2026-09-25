# MPX/3 Rev5 credit model (0.9.4)

The current normative model is REV2-SHARED-CREDIT.md and PROTOCOL.md. Revision 1's shared sum of irrevocable WINDOW entitlements is no longer the resource allocator; Rev5 retains the same Rev4 actual-offset accounting and resource bounds. Weighted changes path capacity selection only.

The `receive_credit_bytes` and `growth_credit_bytes` diagnostics now represent actual unconsumed logical offset commitment, including declared holes, not historical grants. `stream_window_entitlement_bytes` reports the separate sum of grants. `idle_irrevocable_growth_bytes` is a legacy entitlement diagnostic and must not be interpreted as actual rev2 usage; use `idle_actual_data_bytes` and the actual session counters.

A stream that has consumed all of its DATA retains its monotonic WINDOW but uses no actual bootstrap/growth. The sliding bootstrap share can be reused after confirmed consumption, not merely once at the beginning of a stream's lifetime. The 96 MiB growth pool and 128 MiB session limit are enforced at sender and receiver; the 2048-stream bootstrap guarantee totals 32 MiB. Neither DATA ACK nor socket Write completion is application consumption.

A physical page refusal remains possible even within a legal DATA limit. Controls and stream admission have separate bounds. Closing slots are not reusable until their accounting obligations are settled. No pool is increased to conceal starvation.
