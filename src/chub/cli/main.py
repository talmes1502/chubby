"""`chub` CLI Typer app."""

from __future__ import annotations

import typer

from chub.cli.commands import (
    down,
    grep,
    history,
    install_hooks,
    mark_idle,
    note,
    ping,
    recolor,
    register_readonly,
    rename,
    send,
    spawn,
    up,
)
from chub.cli.commands import list as list_cmd

app = typer.Typer(no_args_is_help=True, add_completion=False, rich_markup_mode=None)
app.command(name="up")(up.run)
app.command(name="down")(down.run)
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
