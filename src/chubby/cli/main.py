"""`chubby` CLI Typer app."""

from __future__ import annotations

import typer

from chubby.cli import output
from chubby.cli.commands import (
    attach,
    broadcast,
    detach,
    diag,
    down,
    grep,
    history,
    install_hooks,
    mark_idle,
    note,
    ping,
    preset,
    promote,
    purge,
    recolor,
    register_readonly,
    release,
    rename,
    respawn,
    send,
    spawn,
    start,
    tag,
    tui,
    up,
)
from chubby.cli.commands import list as list_cmd

app = typer.Typer(no_args_is_help=True, add_completion=False, rich_markup_mode=None)


@app.callback()
def _root(
    json_output: bool = typer.Option(
        False, "--json", help="Print results as JSON (auto-on inside agents/CI)."
    ),
    quiet: bool = typer.Option(
        False, "--quiet", help="One id per line for arrays; bare id for objects."
    ),
) -> None:
    """Resolve global output mode before any subcommand runs.

    Without explicit flags the mode is JSON when one of the agent-
    context env vars is set (CLAUDE_CODE, CODEX_CLI, GEMINI_CLI,
    CHUBBY_AGENT, CI, ...) — so an outer agent shelling out to
    chubby always gets parseable output. Pretty mode is the
    interactive default.
    """
    output.configure(json_flag=json_output, quiet_flag=quiet)


app.command(name="up")(up.run)
app.command(name="down")(down.run)
app.command(name="start")(start.run)
app.command(name="ping")(ping.run)
app.command(name="list")(list_cmd.run)
app.command(name="rename")(rename.run)
app.command(name="recolor")(recolor.run)
app.command(name="send")(send.run)
app.command(name="spawn")(spawn.run)
app.command(name="grep")(grep.run)
app.command(name="register-readonly")(register_readonly.run)
app.command(name="mark-idle")(mark_idle.run)
app.command(name="install-hooks")(install_hooks.run)
app.command(name="history")(history.run)
app.command(name="note")(note.run)
app.command(name="respawn")(respawn.run)
app.command(name="tag")(tag.run)
app.command(name="broadcast")(broadcast.run)
app.command(name="attach")(attach.run)
app.command(name="promote")(promote.run)
app.command(name="detach")(detach.run)
app.command(name="release")(release.run)
app.command(name="tui")(tui.run)
app.command(name="purge")(purge.run)
app.command(name="diag")(diag.run)
# `chubby preset` is a sub-app (list/create/delete/show/apply) so the
# command surface stays tidy. Verb-noun shape mirrors `superset` and
# `gh`; presets are saved templates for `chubby spawn`.
app.add_typer(preset.app, name="preset")
