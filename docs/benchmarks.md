Measured with hyperfine 1.20.0 on Darwin arm64, go1.27.1, lmux v1.1.0 (f40f7e9 2026-09-19T21:20:26Z) darwin/arm64.

| Command | Mean [ms] | Min [ms] | Max [ms] | User [ms] | System [ms] | Relative |
|:---|---:|---:|---:|---:|---:|---:|
| `go hello world` | 2.2 ± 0.2 | 1.9 | 3.0 | 1.0 | 0.9 | 1.00 |
| `lmux version` | 5.9 ± 0.5 | 5.2 | 7.8 | 3.4 | 2.0 | 2.62 |
| `lmux hook Stop` | 5.6 ± 0.3 | 5.1 | 6.8 | 3.3 | 1.9 | 2.51 |
| `lmux statusline` | 5.7 ± 0.3 | 5.3 | 7.8 | 3.3 | 1.9 | 2.55 |

| Inside a workspace | Mean [ms] |
|:---|---:|
| `agents rail redraw, 40 agents` | 0.3 |
| `agents rail, one reading` | 0.3 |
| `teammate pane taken over` | 24.7 |
