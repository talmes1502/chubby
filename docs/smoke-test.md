# Smoke test (manual, against real Claude)

Run before every release.

```bash
# 1. clean slate
rm -rf /tmp/chubby-smoke
export CHUBBY_HOME=/tmp/chubby-smoke

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

# 7. cleanup
chubby down
```

If step 6 finds "4", smoke test passes. CI does not run this — it requires Claude auth and real API cost.
