"""`chub` CLI Typer app."""

from __future__ import annotations

import typer

from chub.cli.commands import (
    attach,
    broadcast,
    detach,
    down,
    grep,
    history,
    install_hooks,
    mark_idle,
    note,
    ping,
    promote,
    purge,
    recolor,
    register_readonly,
    rename,
    respawn,
    send,
    spawn,
    tag,
    tui,
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
app.command(name="respawn")(respawn.run)
app.command(name="tag")(tag.run)
app.command(name="broadcast")(broadcast.run)
app.command(name="attach")(attach.run)
app.command(name="promote")(promote.run)
app.command(name="detach")(detach.run)
app.command(name="tui")(tui.run)
app.command(name="purge")(purge.run)
