#!/usr/bin/env python3
"""Exercise path resolution and menu-to-Explorer arguments without opening windows."""
import base64
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import shlex
import subprocess
import sys
sys.dont_write_bytecode = True
import tempfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('pane_menu', str(ROOT / 'cockpit-pane-menu'))
spec = importlib.util.spec_from_loader(loader.name, loader)
menu = importlib.util.module_from_spec(spec)
loader.exec_module(menu)

with tempfile.TemporaryDirectory() as tmp:
    cwd = Path(tmp)
    folder = cwd / 'space & apostrophe\'s #folder'
    folder.mkdir()
    file = folder / 'report.txt'
    file.touch()
    assert menu.hovered_word('see /tmp/a.txt:12 next', 7) == '/tmp/a.txt:12'
    assert menu.hovered_word('中 /tmp/a.txt', 6) == '/tmp/a.txt'
    for text in (str(file), file.as_uri(), str(file) + ':12:3'):
        assert menu.filesystem_path(text, tmp) == file, text
    assert menu.filesystem_path('./' + folder.name, tmp) == folder
    assert menu.filesystem_path('https://example.com' + str(file), tmp) is None
    assert menu.filesystem_path('file://other-host' + str(file), tmp) is None
    assert menu.filesystem_path('/missing/cockpit-menu-test', tmp) is None
    with patch.dict(os.environ, WSL_DISTRO_NAME='Ubuntu'):
        assert menu.filesystem_path('file://wsl.localhost/Ubuntu' + file.as_uri()[7:], tmp) == file
        assert menu.filesystem_path('file://wsl.localhost/Other' + str(file), tmp) is None
    with patch.object(subprocess, 'check_output', return_value=str(file) + '\n') as convert:
        assert menu.filesystem_path('file:///C:/Documents/report.txt', tmp) == file
        assert convert.call_args.args[0] == ['wslpath', '-u', 'C:/Documents/report.txt']
    for path in (file, folder):
        with patch.object(subprocess, 'check_output', return_value='C:\\Documents\\report.txt\n'), \
             patch.object(menu.shutil, 'which', return_value='explorer.exe'), \
             patch.object(subprocess, 'run') as launch:
            menu.open_explorer(path)
            want = ['explorer.exe'] + ([] if path.is_dir() else ['/select,']) + ['C:\\Documents\\report.txt']
            assert launch.call_args.args[0] == want
    args = ['cockpit-pane-menu', '--socket', '/tmp/test', '--client', '/dev/pts/test',
            '--pane', '%1', '--link', file.as_uri(), '--cwd', tmp, '--x', '10', '--y', '4']
    with patch('sys.argv', args), patch.object(subprocess, 'run') as run:
        menu.main()
        call = run.call_args.args[0]
        assert 'Open in Explorer' in call
        command = call[call.index('Open in Explorer') + 2]
        shell = shlex.split(command)[1]
        opener = shlex.split(shell)
        assert Path(os.fsdecode(base64.urlsafe_b64decode(opener[-1]))) == file
        assert str(file) not in command
        assert 'Kill (undoable)' in call and 'Restart' in call
    with patch('sys.argv', args + ['--link', 'https://example.com']), patch.object(subprocess, 'run') as run:
        menu.main()
        assert 'Open in Explorer' not in run.call_args.args[0]
print('PASS: filesystem URLs, WSL and Windows paths, line suffixes, literal filenames, menu token and Explorer file/directory arguments')
