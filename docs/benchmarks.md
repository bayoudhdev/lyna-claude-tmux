Measured with hyperfine 1.20.0 on Darwin arm64, go1.27.1, lmux v1.2.0 (cc08c00 2026-09-20T12:49:21Z) darwin/arm64.

| Command | Mean [ms] | Min [ms] | Max [ms] | User [ms] | System [ms] | Relative |
|:---|---:|---:|---:|---:|---:|---:|
| `go hello world` | 2.0 ± 0.1 | 1.9 | 2.6 | 0.9 | 0.8 | 1.00 |
| `lmux version` | 5.6 ± 0.3 | 5.2 | 7.3 | 3.2 | 1.8 | 2.78 |
| `lmux hook Stop` | 5.6 ± 0.3 | 5.2 | 8.2 | 3.2 | 1.9 | 2.80 |
| `lmux statusline` | 6.0 ± 0.4 | 5.4 | 7.9 | 3.4 | 2.0 | 2.97 |

| Inside a workspace | Mean [ms] |
|:---|---:|
| `agents rail redraw, 40 agents` | 0.3 |
| `agents rail, one reading` | 0.3 |
| `teammate pane taken over` | 26.4 |
| `git workstation redraw, 400 commits` | 1.7 |
| `git workstation, one reading` | 1.9 |
