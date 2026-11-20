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
    assert menu.hovered_span('see /tmp/a.txt:12 next', 7) == (4, 17)
    assert menu.hovered_span('中 /tmp/a.txt', 6) == (2, 12)
    assert menu.hovered_span('see  /tmp', 4) is None
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
            '--pane', '%1', '--link', file.as_uri(), '--cwd', tmp, '--x', '10', '--y', '4',
            '--left', '51', '--top', '16']
    with patch('sys.argv', args), patch.object(subprocess, 'run') as run:
        menu.main()
        call = run.call_args.args[0]
        assert call[call.index('-x') + 1] == '61'
        assert call[call.index('-y') + 1] == '20'
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
    # Paths wrapped across lines: by the terminal at the pane edge (no indent) and by an
    # agent's own renderer (indented continuation), over two and three lines.
    deep = cwd / 'wrapped-directory-name' / 'second-level' / 'final-report.txt'
    deep.parent.mkdir(parents=True)
    deep.touch()
    whole = str(deep)
    head, middle, tail = whole[:12], whole[12:30], whole[30:]
    def first_hit(lines, row, column):
        span = menu.hovered_span(lines[row], column)
        return next(filter(None, (menu.filesystem_path(t, tmp) for t in menu.wrapped_candidates(lines, row, span))), None)
    soft = ['see ' + head, middle, tail + ' and more']
    assert first_hit(soft, 0, 5) == deep            # click the first piece
    assert first_hit(soft, 1, 2) == deep            # the middle line, all one piece
    assert first_hit(soft, 2, 1) == deep            # the last piece
    hard = ['  Saved to ' + whole[:20], '  ' + whole[20:] + '.']
    assert first_hit(hard, 0, 12) == deep and first_hit(hard, 1, 3) == deep
    tilde = ['read ~/.', 'claude-beta/x and set']
    assert menu.wrapped_candidates(tilde, 1, (0, 13))[0] == '~/.claude-beta/x'
    # Not at a line edge: no joining. A join that does not exist falls back to the piece.
    assert menu.wrapped_candidates(['a ' + whole + ' b', 'next'], 0, (2, 2 + len(whole))) == [whole]
    assert first_hit([whole, 'unrelated words'], 0, 3) == deep
    assert first_hit(['words', whole], 1, 3) == deep
    def refuse_drive_paths(command, **kwargs):
        raise subprocess.CalledProcessError(1, command)
    with patch.object(subprocess, 'check_output', side_effect=refuse_drive_paths):
        assert menu.first_existing(['C:\\bad' + whole, whole], tmp) == deep  # one bad join is skipped

    # The clicked row's neighbours come from capture-pane, located by the clicked line's text.
    screen = ['filler'] * 5 + soft + ['after']
    def captured(lines):
        return patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '\n'.join(lines) + '\n'))
    with captured(screen[:9]) as run:      # row 6 of a 9-row pane, no copy mode: rows ..6+2
        lines, row = menu.screen_lines(['tmux'], '%1', 6, 0, 9, soft[1])
        assert lines[row] == soft[1] and lines[row - 1] == soft[0] and lines[row + 1] == soft[2]
        assert run.call_args.args[0][-4:] == ['-S', str(6 - menu.SNAPSHOT_SEARCH), '-E', '8']
    # Copy mode: scrolled up 3, and the live screen printed 2 more lines after entry,
    # so the clicked row is 2 rows above where it would otherwise be.
    # The capture ends 4 rows below the expected row (index 6); the clicked line is at 4.
    shifted = ['filler'] * 3 + soft + ['after', 'new 1', 'new 2', 'more', 'more']
    assert shifted[len(shifted) - 1 - 4] == 'after' and shifted[4] == soft[1]
    with captured(shifted) as run:
        lines, row = menu.screen_lines(['tmux'], '%1', 9, 3, 12, soft[1])
        assert lines[row - 1:row + 2] == soft, lines
        assert run.call_args.args[0][-1] == '10'
    # Copy mode's top row as tmux reports it, with the position indicator over its end.
    edge = ['see ' + whole[:60], whole[60:]]
    overlaid = edge[0][:-16] + '11:38:32 [37/40]'
    with captured(['filler'] * 3 + edge + ['after'] + ['late'] * 3):  # 4 rows below, as asked
        lines, row = menu.screen_lines(['tmux'], '%1', 0, 37, 28, overlaid)
        assert lines[row:row + 2] == edge, lines
        assert first_hit(lines, row, 6) == deep
        assert menu.screen_lines(['tmux'], '%1', 1, 37, 28, overlaid) == ([overlaid], 0)  # top row only
        assert menu.screen_lines(['tmux'], '%1', 0, None, 28, overlaid) == ([overlaid], 0)  # copy mode only
    with captured(['nothing like it']):
        assert menu.screen_lines(['tmux'], '%1', 0, 0, 5, 'clicked') == (['clicked'], 0)
    with captured(['x']) as run:            # the bottom row: never ask for rows below it
        menu.screen_lines(['tmux'], '%1', 4, 0, 5, 'x')
        assert run.call_args.args[0][-1] == '4'

    # End to end through main(): a click on the second piece offers the whole path.
    def fake_run(command, **kwargs):
        if 'capture-pane' in command:
            return subprocess.CompletedProcess(command, 0, '\n'.join(soft) + '\n')
        return subprocess.CompletedProcess(command, 0)
    args = ['cockpit-pane-menu', '--socket', '/tmp/test', '--client', '/dev/pts/test', '--pane', '%1',
            '--line', soft[1], '--x', '2', '--y', '1', '--height', '3', '--scroll', '', '--cwd', tmp]
    with patch('sys.argv', args), patch.object(subprocess, 'run', side_effect=fake_run) as run:
        menu.main()
        call = run.call_args.args[0]
        command = call[call.index('Open in Explorer') + 2]
        token = shlex.split(shlex.split(command)[1])[-1]
        assert Path(os.fsdecode(base64.urlsafe_b64decode(token))) == deep
    with patch('sys.argv', args[:args.index('--x')] + ['--x', '40', '--y', '1']), \
         patch.object(subprocess, 'run', side_effect=fake_run) as run:
        menu.main()                          # a click past the text offers no Explorer entry
        assert 'Open in Explorer' not in run.call_args.args[0]
print('PASS: filesystem URLs, WSL and Windows paths, line suffixes, literal filenames, wrapped paths, copy-mode row lookup, menu token and Explorer file/directory arguments')
