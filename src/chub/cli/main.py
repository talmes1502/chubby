"""`chub` CLI Typer app."""

from __future__ import annotations

import typer

from chub.cli.commands import down, ping, recolor, rename, send, spawn, up
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
