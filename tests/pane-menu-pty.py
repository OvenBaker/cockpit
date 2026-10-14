#!/usr/bin/env python3
"""Drive Cockpit's actual right-click binding on an isolated tmux server."""
import fcntl
import json
import os
from pathlib import Path
import pty
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
    env = dict(os.environ, PATH=str(tmp) + ':' + os.environ['PATH'], TERM='xterm-256color', TMUX='')
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
        print('PASS: actual mouse binding resolves OSC 8 target intact, launches Explorer selection, hides action for web links, recognises printed directory paths, preserves kill/restart confirmations, and zooms the clicked pane')
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
