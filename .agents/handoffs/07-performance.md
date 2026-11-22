# Performance measurements

Synthetic in-memory SQLite benchmarks on Apple M2 / darwin arm64, Go 1.27.3,
five measured iterations, deterministic globally distributed points; nearby
query centered at (35.7, 51.4), radius 2.5 km:

| Rows | R-Tree | Full scan |
| ---: | ---: | ---: |
| 10,000 | 0.049 ms, 1.9 KB, 48 allocs | 4.22 ms, 320 KB, 30,011 allocs |
| 100,000 | 0.052 ms, 1.9 KB, 48 allocs | 42.9 ms, 3.2 MB, 300,011 allocs |
| 1,000,000 | 0.054 ms, 1.9 KB, 48 allocs | 428.7 ms, 32.0 MB, 3,000,011 allocs |

The benchmark uses the SQLite R-Tree first in the query plan and performs
Haversine filtering. These are indexed-vs-scan measurements only; no disk,
concurrent API, process memory, startup, provider volume, or legacy Node
side-by-side claims are made.
