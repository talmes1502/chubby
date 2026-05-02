# Embedded Claude PTY — Detailed Plan

**Goal:** replace chubby's parsed-JSONL conversation pane with a live PTY view of the focused claude session, while keeping every chubby orchestration feature intact (rail, folders, broadcast, search, hub-runs, /detach, …). Claude renders its own UI inside chubby's frame; chubby stays a thin multiplexer.

**Status:** plan, not implemented.

**Approach (from research):**
- Per-session PTY stays — `chubby-claude` already opens one. Re-route its output into a virtual-terminal emulator inside the TUI instead of (only) stdout.
- VT emulator: **`github.com/charmbracelet/x/vt`** (active 2026-04-30, same author as Bubble Tea, ANSI dialect compatible).
- Reference architecture: **`Gaurav-Gosain/tuios`** — production Bubble Tea terminal multiplexer that already solved this exact problem.
- Render the vt screen as styled lines (SGR + text), **never** pass cursor-positioning escapes through to Bubble Tea — they fight the parent renderer.

---

## What we keep (chubby's actual value-add)

These do NOT change in this pivot. The PTY-embed only swaps out the conversation pane.

| Feature | Stays as-is |
|---|---|
| Multi-session rail (folders, status glyphs, focus arrow, search filter) | yes |
| Compose bar with @-name redirect, slash-command popup, ghost text, /chub commands | yes — keystrokes target the focused session's PTY |
| Broadcast modal — Ctrl+B selects N sessions, Enter sends to all | yes — writes to N PTYs in parallel |
| `chubby spawn` / respawn / `/detach` / Ctrl+A attach picker | yes — spawn flow unchanged |
| Folders (chubby-side state, `folders.json`) | yes |
| FTS transcript search (Ctrl+G), hub-run history (Ctrl+H) | yes — JSONL still indexed |
| File editor pane (Ctrl+O / Ctrl+E) | yes |
| Status spinner glyph in the rail (`⠋⠙⠹…` for THINKING) | yes — JSONL tailer still drives `Session.Status`; only the BANNER goes away |
| Stuck-thinking sweep, daemon health, sessions DB | yes |

## What we drop (the "re-implementing claude" surface)

- `tui/internal/views/markdown.go` (glamour theme + chubby palette)
- `tui/internal/views/toolbox.go` (rounded tool-call boxes)
- `tui/internal/model/banner_view.go` (`✢ Thinking…` activity line, two-line banner — replaced by claude's own status row)
- `tui/internal/model/viewport_view.go` (`renderViewport*`, `renderTurns`, `stylePathMentions`, `pathStyle`, `pathRE`)
- `Turn.Tools` / `ToolCall` plumbing in the TUI side (still parsed by daemon for FTS, but the rendering path goes away)
- `transcript_message` event subscription on the TUI side (daemon keeps emitting; TUI just stops listening for it)
- `tool_result` event subscription (same)
- `session_usage_changed` event subscription on the TUI side (claude renders its own token counter in its status row)
- `generationStartedAt` / `thinkingStartedAt` maps in the Model (still needed daemon-side for the rail glyph; TUI no longer tracks)

## Rough size estimate

- **Code added:** ~600-900 lines (PTY plumbing, vt emulator wrapper, paint loop, key encoder, debounce, tests)
- **Code removed:** ~1,400 lines (markdown.go, toolbox.go, banner_view.go, viewport_view.go, related tests, generation/thinking timer state)
- **Net:** roughly -500 to -800 lines, with most of the new code being thin wrappers around upstream libraries.

---

## Architecture

```
┌──────── chubby-tui (Bubble Tea, single-process) ────────┐
│                                                         │
│  ┌─Rail──────┐   ┌─Conversation pane (NEW)─────────┐    │
│  │ ┃ alpha   │   │  styled lines from vt.Emulator  │    │
│  │   beta    │   │  cursor: reverse-video cell    │    │
│  │   gamma ⠹ │   │                                 │    │
│  └───────────┘   └─────────────────────────────────┘    │
│  ┌─Compose bar (unchanged)─────────────────────────┐    │
│  │ @alpha ▸ type prompt …                          │    │
│  └─────────────────────────────────────────────────┘    │
└──────┬──────────────────────────────────────┬───────────┘
       │ keystrokes routed to focused PTY    │ tea.Msg(paneDirtyMsg) on screen change
       ▼                                      ▲
   ┌─chubbyd (Python daemon, unchanged)──────┴───┐
   │   register_wrapped, inject, list_sessions,  │
   │   broadcast events, JSONL tailer (rail glyph │
   │   + FTS), folders, sweep, etc.              │
   └─────────────────────────────────────────────┘
       ▲                                      ▲
       │ register / inject_to_pty             │ JSONL tail (state for rail)
   ┌───┴──────────────────────────────────────┴─┐
   │  chubby-claude wrappers (one per session)  │
   │  - spawns claude with PTY                  │
   │  - exposes PTY bytes to the TUI            │
   └────────────────────────────────────────────┘
```

### Key change: where do the PTY bytes go?

Today `chubby-claude` spawns `claude` and pipes the PTY to the wrapper's stdout/stdin (so the user can run `chubby-claude` standalone). Then the chubby tui calls `inject_to_pty` to write keystrokes through the daemon.

For embedded mode, the chubby tui needs **read access** to each session's PTY bytes too. Two viable shapes:

**Option A (recommended): daemon proxies PTY output**

- Wrapper streams PTY chunks to the daemon over the existing socket as `pty_output` events.
- Daemon broadcasts `pty_output { session_id, chunk_b64 }` to TUI subscribers.
- TUI feeds chunks into the per-session vt emulator.
- Symmetric to the existing `inject_to_pty` (TUI → daemon → wrapper → PTY) — just reversed.
- Pros: single-socket architecture, no extra connections, multi-TUI support comes free (two chubby tui instances see the same PTY stream), wrapper can still run standalone.
- Cons: ~10-15ms latency per chunk through the daemon. Mitigated by chunk batching (read 4 KB / 16 ms windows).

**Option B: TUI dials each wrapper directly**

- Each wrapper exposes its own Unix socket; TUI multiplexes connections.
- Lower latency, more connections, more failure modes.
- More plumbing for spawn/dial/teardown.

→ **Go with A.** The latency is invisible (claude is human-paced), the architecture stays simple, and multi-TUI support is a nice future bonus.

### Rendering loop

1. **Per-session emulator state.** When the TUI first sees a session, allocate a `*vt.Emulator` sized to the current conversation-pane dimensions (cols × rows). Persist across focus changes — switching focus does NOT reset the emulator; we just stop rendering it.
2. **Per-session goroutine.** A daemon-side reader receives `pty_output` events for the session, calls `em.Write(chunk)`, and posts a debounced `paneDirtyMsg{sid}` (~30 Hz cap). Don't post one msg per chunk — that floods the tea event loop.
3. **`View()` for the conversation pane.** When the focused session has an emulator, call `em.RenderLines()` (or equivalent — render the screen as N styled lines, no CUP escapes), then wrap in the rounded lipgloss frame. When focused session has NO emulator yet (rare race), fall back to "(connecting…)".
4. **Cursor.** vt exposes `CursorPosition()`. Render the cell at that position in reverse-video so the user sees claude's cursor inside chubby's frame. Bubble Tea's altscreen hides the real terminal cursor; we own the visible cursor.

### Key routing

- When `m.activePane == PaneConversation` AND `m.mode == ModeMain`:
  - `tea.KeyMsg` → encode to bytes (Enter→`\r`, Up→`ESC[A`, etc. — cribbed from `bubbletea` source / a small key-to-bytes table) → `inject_to_pty(focused_sid, bytes)`.
  - **Reserve chubby-level chords** so the user can still escape PTY-mode: `Ctrl+\` (cycle focus), `Tab` (switch pane), `Esc` (back to default behavior). The compose bar still owns `Ctrl+B`/`Ctrl+H`/`Ctrl+A`/etc.
- When the compose bar has text, Enter still sends to the inject path (unchanged).
- When `m.activePane == PaneRail`, key handling is unchanged.

### Resize

- On `tea.WindowSizeMsg`:
  1. Recompute conversation-pane dimensions (already done in `recomputeViewportGeom`).
  2. For every session emulator: `em.Resize(cols, rows)`.
  3. Send a daemon RPC `resize_pty(session_id, cols, rows)` for each session — the daemon forwards to the wrapper, the wrapper calls `pty.Setsize`, the kernel SIGWINCHes claude.

---

## Phase plan

### Phase 0 — Spike (1 day, throwaway code, validates the approach)

**Goal:** prove `charmbracelet/x/vt` + a Bubble Tea pane can render a live `claude` PTY without the cursor-positioning fights mentioned in the research brief.

- [ ] New branch, new tiny program `cmd/spike-pty/main.go` outside chubby's tree.
- [ ] Spawn `bash -c 'claude'` (or just `bash`) with `creack/pty.StartWithSize`.
- [ ] Wire one `vt.Emulator`, one Bubble Tea program with a single pane that renders `em.RenderLines()` and pipes keystrokes back to PTY.
- [ ] Verify visually: claude's startup banner renders, typing works, markdown renders correctly, scrollback works, the cursor is visible.
- [ ] **Decision gate:** if the spike works cleanly, proceed to Phase 1. If it has render artifacts we can't solve in a day, escalate — the rest of the plan depends on this primitive working.

### Phase 1 — Daemon side: PTY output proxy (2 days)

- [ ] Add `pty_output` event in `src/chubby/proto/schema.py` (params: `session_id`, `chunk_b64`, `ts`).
- [ ] In `src/chubby/wrapper/main.py`, the existing `pump_pty_to_daemon_and_term` already pipes PTY → terminal. Add a parallel path: when running with `--proxy-output` (set by daemon-spawned wrappers), also send each chunk to the daemon as a `push_output` extension.
- [ ] In `src/chubby/daemon/main.py`, the existing `push_output` handler currently feeds the LogWriter. Extend it to also broadcast `pty_output` to subscribers (with the chunk base64-encoded to keep JSON-RPC happy).
- [ ] Add `resize_pty` RPC: validates session exists & is wrapped, forwards `resize_pty` event to the wrapper, wrapper calls `pty.setwinsize` on its PTY.
- [ ] Tests:
  - [ ] `tests/daemon/test_pty_output_broadcast.py` — wrapper pushes a chunk → subscriber sees `pty_output` with matching b64 payload.
  - [ ] `tests/daemon/test_resize_pty.py` — RPC plumbs through to wrapper.
- [ ] **Acceptance:** an existing `chubby-claude --name foo` started with proxy-output writes its PTY chunks to the daemon, which broadcasts them. Other wrappers and standalone usage are byte-equivalent (no regression).

### Phase 2 — TUI side: PTY pane primitive (2 days)

- [ ] Add `github.com/charmbracelet/x/vt` and `github.com/creack/pty` to `tui/go.mod` (creack/pty only for keystroke encoding helpers — we don't open PTYs in the TUI).
- [ ] New file `tui/internal/ptypane/pane.go`:
  - `type Pane struct { em *vt.Emulator; w, h int }`
  - `Pane.Write(chunk []byte)` → `em.Write(chunk)`, mark dirty
  - `Pane.Resize(w, h int)` → `em.Resize(w, h)`
  - `Pane.View() string` → render styled lines + cursor cell
  - `KeyToBytes(tea.KeyMsg) []byte` → encoder for Enter/arrows/ctrl/etc.
- [ ] Tests in `tui/internal/ptypane/pane_test.go`:
  - [ ] Plain text written renders verbatim
  - [ ] SGR escapes survive through `View()`
  - [ ] Cursor position cell is reverse-video styled
  - [ ] `KeyToBytes` round-trips Enter / arrows / Ctrl-letter
- [ ] **Acceptance:** unit-tested PTY pane primitive, no integration with chubby's Model yet.

### Phase 3 — TUI side: wire pane into Model (2 days)

- [ ] `Model` gets `pty map[string]*ptypane.Pane` keyed by session ID.
- [ ] On `pty_output` event: lookup or lazy-create `Pane` for the session, `Write` chunk, post `paneDirtyMsg` (debounced via a single tick at ~30 Hz to coalesce bursts).
- [ ] On `tea.WindowSizeMsg`: resize every `Pane`. Fire `resize_pty` RPC for every session.
- [ ] Replace `renderViewport*` / `renderTurns` body: when `m.pty[focusedID]` exists, return its `View()` framed in a rounded border + status hint footer; otherwise show "(no session)" placeholder.
- [ ] In `handleKeyMain` (the conversation-pane keystroke branch), when `activePane == PaneConversation` AND no compose text, route `tea.KeyMsg` through `KeyToBytes` → `inject_to_pty` (existing RPC).
- [ ] Tests:
  - [ ] `pty_output` event creates a pane and writes the chunk
  - [ ] WindowSizeMsg resizes panes
  - [ ] KeyMsg in conversation pane fires `inject` (mock client)
- [ ] **Acceptance:** the focused session's claude UI shows live inside chubby's frame; typing works; resize works; rail navigation still works.

### Phase 4 — Rip out the parsed-rendering path (1 day)

- [ ] Delete (or stub):
  - `tui/internal/views/markdown.go` + `_test.go`
  - `tui/internal/views/toolbox.go` + `_test.go`
  - `tui/internal/model/banner_view.go` + tests
  - `tui/internal/model/viewport_view.go` + tests
  - The `transcript_message` / `tool_result` / `session_usage_changed` event handlers in `model.go` (daemon still emits — TUI just doesn't subscribe)
  - `Model.conversation`, `Model.lastUsage`, `Model.thinkingStartedAt`, `Model.generationStartedAt`, `Model.scrollOffset`, `Model.newSinceScroll`, `Model.lastViewportInnerW/H`, `Model.lastViewportLineCount`
  - `Model.appendTranscriptTurn`, `Model.applyToolResult`, `Model.harvestPathsFromText`, `Model.copyConversation`, `Model.formatConversation`
  - `tui/internal/model/scroll.go` (no per-turn scroll anymore — vt emulator owns scrollback)
- [ ] Update `Model.Update` to drop the no-longer-handled events.
- [ ] Update help screen (`viewHelp`) to remove references to per-turn navigation, /detach behavior unchanged.
- [ ] Build, run tests, look for compile errors / dead state references.
- [ ] **Acceptance:** ~1,400 lines deleted, all tests still green.

### Phase 5 — Bring back the small features that DON'T need full re-rendering (1 day)

These were nice-to-haves living in the renderer; some can be re-implemented as overlays on the PTY pane.

- [ ] **Per-session scroll inside the PTY view.** vt has scrollback; expose PgUp/PgDn as `em.ScrollBack(n)` / `ScrollForward(n)`. Bind same keys as before when conversation pane is active and no compose text.
- [ ] **Copy-to-clipboard (Ctrl+Y).** vt exposes the visible screen as text — copy that. Lose the structured markdown; gain accuracy with code blocks.
- [ ] **File-mention detection (Ctrl+]).** Run the existing `pathRE` regex over the rendered text grid (strip ANSI), grab the most-recent match. Same UX, different source.
- [ ] **`/detach` and broadcast** keep working — they're orchestration, not rendering.
- [ ] **Acceptance:** key features survive the pivot in spirit if not in exact UI.

### Phase 6 — Polish + ship (1 day)

- [ ] Update `docs/install.md` and the help screen.
- [ ] Update memory (`project_chub_orchestrator.md`) to reflect the new architecture.
- [ ] Smoke test: spawn 3 sessions, broadcast, type in one, watch claude's permission prompt render natively, answer with `1`, confirm tool runs.
- [ ] Check the rail spinner still pulses while claude is thinking (JSONL tailer is the source — should still work).
- [ ] Tag a commit, push, watch a real workday's worth of usage.

---

## Risks + mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| `charmbracelet/x/vt` doesn't render claude's altscreen sequences cleanly | Medium | **Phase 0 spike** is the gate. If broken, fall back to `taigrr/bubbleterm` (uses a different vt engine internally). |
| Keystroke encoding misses a niche key (e.g. function keys, alt-modifier) | Medium | Crib the table from Bubble Tea's own input layer or `term/teakey`; add as users hit gaps. |
| Latency through the daemon makes typing feel sluggish | Low | Daemon proxy is a Unix socket on localhost; <1 ms per round trip in practice. If a problem, switch to direct wrapper sockets (Option B) — no API change. |
| Losing FTS history search because we stop subscribing to `transcript_message` | Low | The daemon still tails JSONL and indexes into FTS; we only stop *displaying* turns in the TUI. `/grep` keeps working. |
| User wants a structured turn-by-turn view back (markdown rendering) | Low | Could add a "structured" mode toggle later that reads JSONL directly, but YAGNI for now. |
| Multi-TUI users (two chubby tui instances) see desynced PTY state | Low | The daemon-proxy approach makes this work-by-default — both subscribers receive the same `pty_output` stream. Confirm in a smoke test. |

---

## Order of execution

| Phase | Days | Dependency |
|---|---|---|
| 0 — Spike | 1 | none |
| 1 — Daemon PTY proxy | 2 | 0 |
| 2 — PTY pane primitive | 2 | 0 (parallel with 1) |
| 3 — Wire into Model | 2 | 1 + 2 |
| 4 — Rip out renderer | 1 | 3 |
| 5 — Re-add features | 1 | 4 |
| 6 — Polish + ship | 1 | 5 |

**Total: ~10 working days** (call it 2 calendar weeks with normal interruptions).

The spike (Phase 0) is the **decision gate**. If it doesn't work cleanly, we don't proceed — we'd revisit the architecture or stick with the parsed-JSONL approach. Everything else assumes the primitive works, which the research strongly suggests it will (`Gaurav-Gosain/tuios` is a working production-grade reference).

---

## Stop conditions

If, during the spike or Phase 3, ANY of the following happen, halt and re-plan:
- ANSI escape fights make the conversation pane unreadable
- Latency through the daemon is visibly sluggish (>50 ms perceived typing lag)
- Resize breaks claude's layout
- Multi-session focus switching loses or corrupts state
