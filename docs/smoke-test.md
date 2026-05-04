# Smoke test (manual, against real Claude)

Run before every release. Exercises the core spawn/send loop plus the headline post-Phase-1+ features (worktrees, lifecycle scripts, presets, agent-context CLI).

```bash
# 1. clean slate
rm -rf /tmp/chubby-smoke
export CHUBBY_HOME=/tmp/chubby-smoke
export CHUBBY_WORKTREES_ROOT=/tmp/chubby-smoke/worktrees

# 2. start daemon
chubby up --detach
sleep 0.5

# 3. spawn a real Claude session
chubby spawn --name smoke --cwd /tmp

# 4. wait for it to register
chubby list   # expect: smoke ○ wrapped /tmp

# 5. send a prompt
chubby send smoke "what is 2+2?"

# 6. wait, then check the log
sleep 5
cat $CHUBBY_HOME/runs/*/logs/smoke.log | grep -i 4

# 7. agent-context output flips to JSON
CLAUDE_CODE=1 chubby list | python3 -c "import json,sys; print(len(json.load(sys.stdin)))"
# expect: 1
chubby list --quiet
# expect: one session id per line

# 8. worktree spawn (requires git on PATH; clean up the test repo after)
mkdir -p /tmp/chubby-smoke/repo && cd /tmp/chubby-smoke/repo
git init -q && echo a > f && git add f && git -c user.email=t@t -c user.name=t commit -qm first
cd -
chubby spawn --name wt --cwd /tmp/chubby-smoke/repo --branch wip-smoke
chubby list   # expect: wt's cwd starts with /tmp/chubby-smoke/worktrees/...
chubby release wt
test ! -d /tmp/chubby-smoke/worktrees/*/wip-smoke   # worktree dir cleaned

# 9. lifecycle script runs on spawn
mkdir -p /tmp/chubby-smoke/lifecycle/.chubby
cat > /tmp/chubby-smoke/lifecycle/.chubby/config.json <<'EOF'
{"setup": ["touch SETUP_RAN"]}
EOF
chubby spawn --name lc --cwd /tmp/chubby-smoke/lifecycle
test -f /tmp/chubby-smoke/lifecycle/SETUP_RAN

# 10. presets round-trip
chubby preset create smoke-preset --cwd /tmp --branch "wip-{date}"
chubby preset list | grep smoke-preset
chubby preset delete smoke-preset

# 11. cleanup
chubby down
```

If steps 6, 7, 8, 9, 10 all succeed, smoke test passes. CI does not run this — it requires Claude auth and real API cost.
