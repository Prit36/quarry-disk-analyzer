# Before/after performance recheck — 18 October 2026

Fresh baseline rebuilt from **1a9e250**, compared with the current uncommitted optimization changes.
Five pairs ran on the same 1,452,146-entry saved scan, with run order reversed in alternating pairs.
Both binaries were compiled once with Go 1.27.2 and used identical test harnesses. Worker benchmarks
ran for one second per case. Tables show medians of five runs per version.

Windows/amd64, Intel Core i5-12400F, 12 logical processors. UI measurements are headless and exclude GPU/window presentation.

## UI latency

| Action | Before ms | After ms | Latency change |
|---|---:|---:|---:|
| Display all files | 54.014 | 36.372 | -32.7% |
| Search `*.dll` | 21.844 | 17.162 | -21.4% |
| Search `microsoft` | 34.999 | 27.369 | -21.8% |
| Search folders | 43.387 | 20.756 | -52.2% |
| Search `path:system32 ext:dll` | 22.017 | 17.962 | -18.4% |
| Sort Name | 110.150 | 98.784 | -10.3% |
| Sort Folder | 158.003 | 139.019 | -12.0% |
| Sort Modified | 29.597 | 26.244 | -11.3% |
| Sort Extension | 74.903 | 73.982 | -1.2% |
| Worst of first five frames after Select All | 34.957 | 8.366 | -76.1% |
| Menu sync after invalidating bulk selection facts | 27.000 | Below timer resolution | — |
| Select All command itself | 59.753 | 60.099 | +0.6% |
| File idle frame | 1.000 | 0.999 | -0.1% |
| File scroll frame | 1.236 | 1.002 | -18.9% |
| Treemap hover frame | 1.096 | 1.002 | -8.6% |
| Extensions ready | 25.352 | 25.684 | +1.3% |

## Worker latency and allocations

| Operation | Before ms | After ms | Before MB/op | After MB/op |
|---|---:|---:|---:|---:|
| Sorts/Name | 85.572 | 73.706 | 40.220 | 40.219 |
| Sorts/Folder | 115.352 | 85.725 | 80.691 | 79.245 |
| Sorts/Size | 15.334 | 13.512 | 22.348 | 17.965 |
| Sorts/Modified | 14.673 | 13.572 | 22.347 | 17.965 |
| Sorts/Extension | 49.259 | 48.873 | 40.786 | 40.786 |
| FileQueries/all files | 37.549 | 20.057 | 31.400 | 27.391 |
| FileQueries/*.dll | 11.599 | 8.366 | 2.248 | 2.406 |
| FileQueries/microsoft | 24.722 | 16.071 | 7.585 | 5.043 |
| FileQueries/path:system32_ext:dll | 9.600 | 10.044 | 1.842 | 2.204 |
| FileQueries/type:folder | 33.246 | 9.910 | 19.290 | 8.605 |

## Retained Go heap after GC

| State | Before MiB | After MiB | Saved MiB |
|---|---:|---:|---:|
| Scan installed | 117.95 | 117.95 | 0.00 |
| All files selected | 160.66 | 155.01 | 5.65 |
| Selection reduced to one item | 153.62 | 135.03 | 18.59 |
| End of interaction sequence | 222.12 | 203.53 | 18.59 |

## Run-to-run ranges

| UI action | Before min–max ms | After min–max ms |
|---|---:|---:|---:|
| Display all files | 50.567–59.006 | 35.001–41.007 |
| Search folders | 41.062–45.703 | 19.082–27.490 |
| Sort Folder | 155.799–171.772 | 130.742–153.552 |
| Worst of first five frames after Select All | 32.247–42.849 | 7.239–10.580 |

Negative latency changes mean faster. Small differences around one millisecond can be scheduler noise.
Idle, scrolling and hover frame differences are not evidence of new rendering improvements; those paths are unchanged in this pass.
Select All map construction remains a separate UI-thread cost; the improved frame timing is measured after that command.
The selective path query also prepares metadata eligibility now; its worker cost and allocation tradeoff are shown above.
Heap figures measure retained Go allocations, not process working set or peak memory. No scan-throughput claim is made.

Raw reports and per-run samples: `C:\Users\My_Home\Desktop\projects\neww\build\latency-pass\rerun-20261018-155137`; aggregated in `summary.json`.
Snapshot SHA-256: `d47d7e07795011538cc9c01c5c174e01b281482a10d9bcb28926e95f09d3509d`.
