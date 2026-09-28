#!/usr/bin/env python3
"""Check complete picker-to-launch wiring without executing a provider or live tmux."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory() as folder:
    base = Path(folder)
    repo = base / 'current repo'
    (repo/'subdir').mkdir(parents=True)
    subprocess.run(['git','-C',str(repo),'init','-q'],check=True)
    tmux = base/'tmux-stub'
    tmux.write_text('''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
args=sys.argv[1:]
with open(os.environ['TEST_LOG'],'a') as f:f.write(json.dumps(args)+'\\n')
if args[0]=='display':
 fmt=args[-1]
 print({'#{window_id}':'@1','#{window_name}':'current','#{pane_current_path}':os.environ['TEST_CWD'],'#{pane_id}':'%1'}.get(fmt,''))
elif args[0]=='list-windows':print('@1\\tcurrent\\t1')
''')
    picker=base/'picker-stub'
    picker.write_text('''#!/usr/bin/env python3
import os,sys
from pathlib import Path
rows=sys.stdin.read().splitlines()
title=sys.argv[sys.argv.index('--title')+1]
if 'what?' in title:
 print(os.environ['TEST_TOGGLE']+'\\t'+next(r for r in rows if r.startswith(os.environ['TEST_AGENT']+'\\t')))
elif 'directory' in title:
 assert rows[0].split('\\t')[0] == os.environ['TEST_REPO'],rows
 if os.environ.get('TEST_CANCEL'):sys.exit(1)
 print(rows[0])
else:print(rows[0])
''')
    tmux.chmod(0o755);picker.chmod(0o755)
    log=base/'calls'
    env=dict(os.environ,COCKPIT_TMUX=str(tmux),COCKPIT_SELECT=str(picker),COCKPIT_ACCOUNTS_DIR=str(base/'no-accounts'),
             SANTA_DB=str(base/'absent.db'),COCKPIT_REMOTE_CONTROL='0',COCKPIT_CODEX_REMOTE='1',
             TEST_LOG=str(log),TEST_CWD=str(repo/'subdir'),TEST_REPO=str(repo))
    for agent,toggle,want in [('claude','on','--dangerously-skip-permissions'),('claude','off','--permission-mode auto'),('codex','on','--dangerously-bypass-approvals-and-sandbox')]:
        log.write_text('')
        subprocess.run([str(ROOT/'cockpit-pick'),'new','%1'],env=dict(env,TEST_AGENT=agent,TEST_TOGGLE=toggle),check=True,capture_output=True)
        calls=[json.loads(line) for line in log.read_text().splitlines()]
        launches=[c for c in calls if c[0]=='respawn-pane']
        assert len(launches)==1,calls
        # tmux receives one quoted bash -lc command; the per-start permission switch
        # must reach that command, rather than only appearing in a picker title.
        command=launches[0][-1].replace('\\ ',' ')
        assert want in command,command
        if toggle=='off':assert 'dangerously' not in command
        if agent=='codex':assert '--remote' not in command
    log.write_text('')
    subprocess.run([str(ROOT/'cockpit-pick'),'new','%1'],env=dict(env,TEST_AGENT='claude',TEST_TOGGLE='on',TEST_CANCEL='1'),check=True,capture_output=True)
    calls=[json.loads(line) for line in log.read_text().splitlines()]
    assert not any(c[0] in ('set','respawn-pane','split-window','new-window') for c in calls),calls
print('PASS: full picker flow applies bypass once, resets to auto next start, preserves repository selection, and cancels without mutation')
