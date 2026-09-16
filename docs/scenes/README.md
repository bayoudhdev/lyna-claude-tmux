# Scenes

Every image and animation in the documentation is recorded from a real
terminal by `scripts/record.sh`, driven by one scene file per feature. A scene
says what to run, which keys to send and when to capture the screen; nothing in
the pictures is drawn by hand.

Record one scene:

```bash
cd ~/src/acme-api                      # a project to record against
scripts/record.sh --scene docs/scenes/split.scene \
  --out docs/assets/split.gif --still docs/assets/split.png
```

Record everything, in order, into `docs/assets/`:

```bash
scripts/record-docs.sh --project ~/src/acme-api
```

The recorder needs tmux, python3, a Chrome (or `CHROME_PATH`) and, for
animations, ffmpeg. `lyna-tmux` must be on `PATH`, and the project must be one
Claude Code has been trusted in, otherwise no hook runs and the status bar
shows no agent state.

Two things keep a recording made on a real machine publishable:

- Every capture has the recorder's home directory and account name rewritten
  before it is drawn (`--redact`, passed by `record-docs.sh`). A replacement of
  the same length keeps the columns of a status bar where they were, so the
  driver says so when it is given one of another length.
- `fixtures/bin/claude` answers `agents --json` with a fixed list of jobs for
  the scenes that show the agent picker, which otherwise would show whatever
  the recorder is running. It is inert unless the scene sets
  `LYNA_TMUX_DEMO_AGENTS`, and the agents of the recorded project itself are
  the real ones, so jumping to a pane still works.

The scene directives are documented at the top of `scripts/record.sh`, and
`go test ./test/scripts/ -run TestRecordScenes` parses every scene here.
