# Smoke test (manual, against real Claude)

Run before every release.

```bash
# 1. clean slate
rm -rf /tmp/chub-smoke
export CHUB_HOME=/tmp/chub-smoke

# 2. start daemon
chub up --detach
sleep 0.5

# 3. spawn a real Claude session
chub spawn --name smoke --cwd /tmp

# 4. wait for it to register
chub list   # expect: smoke ○ wrapped /tmp

# 5. send a prompt
chub send smoke "what is 2+2?"

# 6. wait, then check the log
sleep 5
cat $CHUB_HOME/runs/*/logs/smoke.log | grep -i 4

# 7. cleanup
chub down
```

If step 6 finds "4", smoke test passes. CI does not run this — it requires Claude auth and real API cost.
