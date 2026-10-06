# Raw results of the batch-publication prototype (#332)

Produced by `TestJournalAdapterBench` (build tag `journalexp`) on the branch `exp/332-batch-publication`. PostgreSQL 17.6
in Testcontainers on Colima (2 vCPU VM), `fsync=on`, 12 writers, 5 s per run, 3 repetitions, host load recorded in the
`*_progress.txt` files (2.9 to 4.6 during the runs). One variant per process, load gate below 3.0 before each.

| File | What it is |
|---|---|
| `run2_*` | the kept run, with the publisher's stream discovery corrected; `current` is the control |
| `run1_*` | the first run, with a slow stream-discovery query (a GROUP BY over every pending row on every round). Its saturated numbers match run 2: that defect was NOT what limited the publisher |
| `diagnostic_publisher_pool_W1.txt` | W1 saturated, the batch variant with and without a dedicated connection pool for the publisher |

Scenarios: W1-W3 saturated, R300 and R500 offered rates, H1 and H3 a transaction held in the same shard or in another
database, B1 a publisher stalled 10 s at 300 tx/s then resumed. Not run: a held PUBLISHER.
