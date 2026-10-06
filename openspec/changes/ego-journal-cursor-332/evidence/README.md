# Evidence for design.md sections 10-13 (#332)

The code that produced these files (`TestJournalBench`, `TestJournalAdapterBench`, the `journalexp` adapter variants)
is NOT part of this change: it is on the local branch `exp/332-adapter-shard-serialization` (`6d03c1d`). The commands
below reproduce the runs from that branch.

Raw output of `TestJournalBench` (`inttest/flows/eventstore/journal_bench_test.go`), one `BENCH|...` line per
result, exactly as the test printed them. Nothing was edited except dropping the Go test and Testcontainers
log lines around them.

| File | Run |
|---|---|
| `bench_fsync_on.txt` | scenarios 1-5, `fsync=on` |
| `bench_fsync_off.txt` | scenarios 1-5, `fsync=off` |
| `bench_commit_delay.txt` | scenarios 6-7 (artificial 5 ms commit delay), `fsync=on` |
| `adapter_fsync_on.txt` | REAL adapter, current vs experimental (design.md section 12), `fsync=on` |
| `adapter_fsync_off.txt` | same, `fsync=off` |

| `adapter_matrix_fsync_on.txt` | REAL adapter, 5 variants x 11 scenarios (section 13), `fsync=on`; supersedes the numbers of `adapter_fsync_*` where they overlap |
| `adapter_matrix_progress.txt` | host load average at the start and end of each variant of that matrix |

A first run of the 5-variant matrix was DISCARDED: the host load average was 45-58 (another process of the machine
was saturating it) and its numbers were not comparable between variants. The matrix kept was rerun with a gate that
waits for a 1-minute load average below 3.0 before each variant; the progress file shows 2.1-3.0 at each start and
3.8-5.4 at each end. The 2 vCPU VM of Docker is a fraction of a 16-core host, so even that load is not zero
interference; compare variants within the matrix, not against other runs.

The `adapter_*` files come from `TestJournalAdapterBench` (build tag `journalexp`); the config line there prints
the writer count, the 5 s duration and 3 repetitions. 12 writers (not 16): the adapter's pool is a fixed 20
connections shared by writers and readers. The `config` line of the captured adapter files ends in an empty
`goos/cpus=` field: a harness slip, since removed from the test; ignore it.

Line kinds: `config` (server settings and load parameters), `poll-floor` (idle empty-read latency, the floor of the
eligibility delay), `row` (one variant x scenario: throughput mean and min..max over the repetitions, percentiles
pooled over them).

## Configuration

- PostgreSQL 17.6 (`postgres:17.6-alpine`) in a Testcontainers container on Colima (Docker VM: 2 vCPU, about
  1.9 GiB), host darwin/arm64 with 16 CPUs. `synchronous_commit=on`, `shared_buffers=128MB`,
  `max_connections=300`, `fsync` as named by the file.
- 16 writers, one entity each, 3 events per transaction, reader per measured shard polling in a tight loop with
  batch limit 100, 5 s per run, 3 repetitions. Held transaction (scenarios 3-5): 500 ms held, 100 ms gap. Commit
  delay (scenarios 6-7): 5 ms sleep inside the transaction right before commit.
- Prototypes on scratch tables; the production adapter is not involved.

## Reproduce

From `inttest/`, with Docker reachable (here `DOCKER_HOST=unix://$HOME/.colima/default/docker.sock`,
`TESTCONTAINERS_RYUK_DISABLED=true`):

    URD_JOURNAL_BENCH=1 URD_JOURNAL_BENCH_SECONDS=5 URD_JOURNAL_BENCH_REPS=3 URD_JOURNAL_BENCH_FSYNC=on \
      go test ./flows/eventstore/ -run TestJournalBench -count=1 -v -timeout 580s
    # scenarios 6-7 only: add URD_JOURNAL_BENCH_SCENARIOS=6,7

Adapter experiment (section 12), from `inttest/`:

    URD_JOURNAL_BENCH=1 URD_JOURNAL_BENCH_SECONDS=5 URD_JOURNAL_BENCH_REPS=3 URD_JOURNAL_BENCH_FSYNC=on \
      go test -tags journalexp ./flows/eventstore/ -run TestJournalAdapterBench -count=1 -v -timeout 580s

Other knobs: `URD_JOURNAL_BENCH_WRITERS`, `_EVENTS`, `_HOLD_MS`, `_GAP_MS`, `_FSYNC=on|off`.

## Caveats

Absolute figures depend on this machine: the load generator and the database share the VM's 2 vCPUs. Compare
the variants with each other, not with other hardware. The `fsync=off` run of scenario 2 has a wide range for
serialization (2257..3665 tx/s): treat it as noisy.
