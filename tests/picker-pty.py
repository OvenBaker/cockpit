#!/usr/bin/env python3
"""Exercise the picker on a real PTY, without starting providers or touching live tmux."""
import os
from pathlib import Path
import pty
import select
import shlex
import signal
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
BINARY = sys.argv[1]


def run(command, expected, keys):
    pid, fd = pty.fork()
    if pid == 0:
        os.execv('/bin/bash', ['bash', '-c', command])
    output = b''
    sent = False
    deadline = time.monotonic() + 8
    try:
        while time.monotonic() < deadline:
            if select.select([fd], [], [], .05)[0]:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    break
                output += data
                if not sent and expected.encode() in output:
                    os.write(fd, keys)
                    sent = True
            child, status = os.waitpid(pid, os.WNOHANG)
            if child:
                assert sent, output.decode(errors='replace')
                return os.waitstatus_to_exitcode(status), output
        child, status = os.waitpid(pid, os.WNOHANG)
        if child:
            return os.waitstatus_to_exitcode(status), output
        raise AssertionError('PTY timed out: ' + output.decode(errors='replace'))
    finally:
        os.close(fd)
        try:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
        except ProcessLookupError:
            pass


with tempfile.TemporaryDirectory() as tmp:
    tmp = Path(tmp)
    rows = tmp/'rows'
    rows.write_text('claude\t\tClaude\tdefault\n+type\t\tNew path\t\n')
    output = tmp/'choice'
    command = f'cat {shlex.quote(str(rows))} | {shlex.quote(BINARY)} --title Test --hide 1,2 --toggle "Bypass this start" > {shlex.quote(str(output))}'
    rc, _ = run(command, 'Bypass this start', b'\x02\r')
    assert rc == 0 and output.read_text() == 'on\tclaude\t\tClaude\tdefault\n', (rc, output.read_text())
    rc, _ = run(command, 'Bypass this start', b'\r')
    assert rc == 0 and output.read_text().startswith('off\tclaude\t')
    rc, _ = run(command, 'Bypass this start', b'\x02\x03')
    assert rc == 1 and output.read_text() == ''
    command = f'cat {shlex.quote(str(rows))} | {shlex.quote(BINARY)} --new-row +type --new-label "new path" > {shlex.quote(str(output))}'
    rc, _ = run(command, 'Ctrl-N', b'/claude\x0e')
    assert rc == 0 and output.read_text().startswith('+type\t')
    folder = tmp/'start'
    target = folder/'with space'
    target.mkdir(parents=True)
    command = f'source {shlex.quote(str(ROOT/"picker/new-session.sh"))}; cockpit_read_directory {shlex.quote(str(folder))} > {shlex.quote(str(output))}'
    rc, screen = run(command, 'empty cancels', b'\t\r')
    assert rc == 0 and output.read_text().strip() == str(target), (output.read_text(), screen)
print('PASS: PTY checkbox, per-start reset, cancel, filtered shortcut, and Tab completion with spaces')
