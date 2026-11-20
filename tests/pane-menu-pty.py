#!/usr/bin/env python3
"""Drive Cockpit's actual right-click binding on an isolated tmux server."""
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time

ROOT = Path(__file__).resolve().parents[1]

with tempfile.TemporaryDirectory() as tmp:
    tmp = Path(tmp)
    socket = str(tmp / 'tmux.sock')
    logfile = tmp / 'explorer.json'
    target = tmp / "O'Brien & spaces #file.txt"
    target.touch()
    explorer = tmp / 'explorer.exe'
    explorer.write_text('#!/usr/bin/env python3\nimport json,sys\nfrom pathlib import Path\nPath(' + repr(str(logfile)) + ').write_text(json.dumps(sys.argv[1:]))\n')
    explorer.chmod(0o755)
    converter = tmp / 'wslpath'
    converter.write_text('#!/usr/bin/env python3\nimport sys\nprint("C:/test" + sys.argv[-1])\n')
    converter.chmod(0o755)
    env = dict(os.environ, PATH=str(tmp) + ':' + os.environ['PATH'], TERM='xterm-256color', TMUX='',
               COCKPIT_TMUX='tmux -S ' + socket, COCKPIT_SESSION='cockpit', COCKPIT_SOLO_WIDTH='0')
    def tm(*args):
        return subprocess.check_output(['tmux', '-S', socket, *args], env=env, text=True).strip()
    pid = None
    fd = None
    try:
        # A labelled OSC 8 link exercises spaces, apostrophes and literal # characters.
        output = '\033[2J\033[H\033]8;;' + target.as_uri() + '\033\\Filesystem report\033]8;;\033\\\r\nhttps://example.com\r\n' + str(tmp) + '\r\n'
        writer = tmp / 'writer'
        writer.write_text('#!/usr/bin/env python3\nimport sys,time\nsys.stdout.write(' + repr(output) + ');sys.stdout.flush();time.sleep(60)\n')
        writer.chmod(0o755)
        tm('new-session', '-d', '-s', 'cockpit', '-x', '100', '-y', '30', str(writer))
        # Install the same chrome function used on startup/reload, without starting a poller.
        subprocess.run(['bash', '-c', 'source "$1/lib.sh"; HERE=$1; TMUX="tmux -S $2"; source <(sed -n \'/^apply_chrome() {$/,/^}$/p\' "$1/cockpit"); apply_chrome', '_', str(ROOT), socket], env=env, check=True)
        tm('set', '-g', 'status', 'off')
        tm('set', '-g', 'pane-border-status', 'off')
        pid, fd = pty.fork()
        if pid == 0:
            os.execvpe('tmux', ['tmux', '-S', socket, 'attach', '-t', 'cockpit'], env)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 30, 100, 0, 0))
        def read_until(needle, timeout=5):
            data = b''
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                if select.select([fd], [], [], .05)[0]:
                    data += os.read(fd, 65536)
                    if needle in data:
                        return data
            raise AssertionError('Missing ' + repr(needle) + ': ' + repr(data[-4000:]))
        read_until(b'Filesystem report')
        os.write(fd, b'\033[<2;4;1M\033[<2;4;1m')
        read_until(b'Open in Explorer')
        os.write(fd, b'e')
        deadline = time.monotonic() + 5
        activity = b''
        while not logfile.exists() and time.monotonic() < deadline:
            select.select([fd], [], [], .05)
            if select.select([fd], [], [], 0)[0]:
                activity += os.read(fd, 65536)
        assert logfile.exists(), 'Explorer action never launched: ' + repr(activity[-3000:])
        windows = 'C:/test' + str(target)
        assert json.loads(logfile.read_text()) == ['/select,', windows], logfile.read_text()
        time.sleep(.7)
        os.write(fd, b'\033[<2;4;2M\033[<2;4;2m')
        screen = read_until(b'Swap down')
        assert b'Open in Explorer' not in screen, screen
        os.write(fd, b'\033')
        time.sleep(.7)
        os.write(fd, b'\033[<2;4;3M\033[<2;4;3m')
        read_until(b'Open in Explorer')
        logfile.unlink()
        os.write(fd, b'e')
        deadline = time.monotonic() + 5
        while not logfile.exists() and time.monotonic() < deadline:
            time.sleep(.05)
        assert json.loads(logfile.read_text()) == ['C:/test' + str(tmp)]
        time.sleep(.7)
        for key, prompt in ((b'k', b'kill pane 0? (y/n)'), (b'r', b'restart pane 0? (y/n)')):
            os.write(fd, b'\033[<2;4;2M\033[<2;4;2m')
            read_until(b'Swap down')
            os.write(fd, key)
            read_until(prompt)
            os.write(fd, b'n')
            time.sleep(.7)
            assert tm('list-panes', '-F', '#{pane_id}') == '%0'
        # Right-clicking another pane must act on that pane, regardless of prior focus.
        tm('split-window', '-h', '-t', '%0', 'sleep 60')
        assert tm('display-message', '-p', '#{pane_id}') != '%0'
        time.sleep(.7)
        os.write(fd, b'\033[<2;4;2M\033[<2;4;2m')
        read_until(b'Swap down')
        os.write(fd, b'z')
        time.sleep(.7)
        assert tm('display-message', '-p', '#{pane_id} #{window_zoomed_flag}') == '%0 1'
        tm('resize-pane', '-Z', '-t', '%0')
        tm('respawn-pane', '-k', '-t', '%1', str(writer))
        time.sleep(.7)
        left = int(tm('display-message', '-p', '-t', '%1', '#{pane_left}'))
        # A printed path in the RIGHT pane must be recognised and the menu must
        # appear beside that click, rather than at the same column in the left pane.
        x = left + 4  # SGR coordinates are one-based.
        os.write(fd, f'\033[<2;{x};3M\033[<2;{x};3m'.encode())
        screen = read_until(b'Swap down')
        positions = re.findall(rb'\x1b\[(\d+);(\d+)H(?=\xe2\x94\x8c)', screen)
        assert positions and int(positions[-1][1]) == x, (left, positions)
        assert b'Open in Explorer' in screen, screen
        os.write(fd, b'\033')
        time.sleep(.7)
        # Exercise nonzero row AND column origins with Cockpit's normal status
        # line and pane title borders, including a labelled file hyperlink.
        tm('set', '-g', 'status', 'on')
        tm('set', '-g', 'pane-border-status', 'top')
        tm('split-window', '-v', '-t', '%1', str(writer))
        time.sleep(.7)
        left, top = map(int, tm('display-message', '-p', '-t', '%2', '#{pane_left} #{pane_top}').split())
        assert left > 0 and top > 0
        for row, expected in ((1, ['/select,', windows]), (3, ['C:/test' + str(tmp)])):
            x, y = left + 4, top + row
            os.write(fd, f'\033[<2;{x};{y}M\033[<2;{x};{y}m'.encode())
            screen = read_until(b'Swap down')
            positions = re.findall(rb'\x1b\[(\d+);(\d+)H(?=\xe2\x94\x8c)', screen)
            assert positions and int(positions[-1][1]) == x and int(positions[-1][0]) > 1, positions
            assert b'Open in Explorer' in screen, screen
            logfile.unlink()
            os.write(fd, b'e')
            deadline = time.monotonic() + 5
            while not logfile.exists() and time.monotonic() < deadline:
                time.sleep(.05)
            assert json.loads(logfile.read_text()) == expected
            time.sleep(.7)
        # Wrapped paths, clicked while scrolled back in copy mode. One path is wrapped by the
        # terminal at the pane edge, the other by the writer itself with an indented
        # continuation (as agents print). New output arrives after copy mode is entered, so
        # the live screen no longer lines up with the frozen view that was clicked.
        deep = tmp / ('a' * 40) / ('b' * 40) / 'wrapped-target.txt'
        deep.parent.mkdir(parents=True)
        deep.touch()
        whole = str(deep)
        assert len('see ' + whole) > 100
        wrapped = ('see ' + whole + '\r\n  Saved to ' + whole[:30] + '\r\n  ' + whole[30:] + '\r\n'
                   + ''.join(f'filler {n}\r\n' for n in range(60)))
        writer2 = tmp / 'writer2'
        writer2.write_text('#!/usr/bin/env python3\nimport sys,time\nsys.stdout.write(' + repr(wrapped)
                           + ');sys.stdout.flush();time.sleep(2.5)\nsys.stdout.write("late 1\\r\\nlate 2\\r\\nlate 3\\r\\n");sys.stdout.flush();time.sleep(60)\n')
        writer2.chmod(0o755)
        pane = tm('new-window', '-P', '-F', '#{pane_id}', '-t', 'cockpit', str(writer2))
        read_until(b'filler 59')
        tm('copy-mode', '-t', pane)
        tm('send-keys', '-t', pane, '-X', 'history-top')
        time.sleep(3)
        assert tm('capture-pane', '-p', '-t', pane).splitlines()[-1].startswith('late 3')
        left, top = map(int, tm('display-message', '-p', '-t', pane, '#{pane_left} #{pane_top}').split())
        for row, column in ((1, 3), (3, 5), (0, 8), (2, 15)):
            x, y = left + column + 1, top + row + 1
            os.write(fd, f'\033[<2;{x};{y}M\033[<2;{x};{y}m'.encode())
            screen = read_until(b'Swap down')
            assert b'Open in Explorer' in screen, (row, screen[-2000:])
            logfile.unlink(missing_ok=True)
            os.write(fd, b'e')
            deadline = time.monotonic() + 5
            while not logfile.exists() and time.monotonic() < deadline:
                time.sleep(.05)
            assert json.loads(logfile.read_text()) == ['/select,', 'C:/test' + whole], (row, logfile.read_text())
            time.sleep(.7)
        assert tm('display-message', '-p', '-t', pane, '#{pane_in_mode}') == '1'
        print('PASS: filesystem actions and menu placement in left, right and lower-right panes; labelled links, printed paths, paths wrapped at the edge or indented (scrolled back in copy mode), confirmations and clicked-pane actions')
    finally:
        subprocess.run(['tmux', '-S', socket, 'kill-server'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if fd is not None:
            os.close(fd)
        if pid:
            try:
                os.kill(pid, signal.SIGKILL)
                os.waitpid(pid, 0)
            except ProcessLookupError:
                pass
