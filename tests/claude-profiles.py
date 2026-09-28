#!/usr/bin/env python3
import json, os, pathlib, subprocess, tempfile, unittest
TOOL = pathlib.Path(__file__).resolve().parents[1] / 'claude-profile'
class Profiles(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
  self.home=pathlib.Path(self.tmp.name);self.reg=self.home/'.config/cockpit/accounts';self.reg.mkdir(parents=True)
  self.beta=self.home/'beta';self.beta.mkdir();(self.reg/'work2.configdir').write_text(str(self.beta))
  self.env=os.environ|{'HOME':str(self.home),'COCKPIT_ACCOUNTS_DIR':str(self.reg)}
 def run_tool(self,*args):return subprocess.run([str(TOOL),*args],env=self.env,text=True,capture_output=True)
 def login(self):(self.beta/'.credentials.json').write_text(json.dumps({'claudeAiOauth':{'accessToken':'fixture-only'}}))
 def test_missing_login_refuses_without_fallback(self):
  r=self.run_tool('--account','work2','env');self.assertNotEqual(r.returncode,0);self.assertNotIn('CLAUDE_CONFIG_DIR=',r.stdout)
 def test_new_beta_and_default_environments(self):
  self.login();r=self.run_tool('--account','work2','env');self.assertEqual(r.returncode,0,r.stderr)
  self.assertIn('CLAUDE_CONFIG_DIR='+str(self.beta),r.stdout);self.assertNotIn('fixture-only',r.stdout)
  r=self.run_tool('env');self.assertIn('CLAUDE_CONFIG_DIR\n',r.stdout);self.assertNotIn('CLAUDE_CONFIG_DIR=',r.stdout)
 def test_legacy_resume_retains_root_and_auth(self):
  p=self.home/'.claude/projects/project/old-session.jsonl';p.parent.mkdir(parents=True);p.write_text('{}\n')
  token=self.reg/'work2.token';token.write_text('fixture-token');token.chmod(0o600)
  r=self.run_tool('--account','work2','--session','old-session','env');self.assertEqual(r.returncode,0,r.stderr)
  self.assertIn('CLAUDE_CONFIG_DIR\n',r.stdout);self.assertIn('CLAUDE_CODE_OAUTH_TOKEN=fixture-token',r.stdout)
 def test_wrong_account_resume_and_duplicate_refused(self):
  self.login();p=self.beta/'projects/project/new-session.jsonl';p.parent.mkdir(parents=True);p.write_text('{}\n')
  self.assertNotEqual(self.run_tool('--session','new-session','env').returncode,0)
  p=self.home/'.claude/projects/project/new-session.jsonl';p.parent.mkdir(parents=True);p.write_text('{}\n')
  self.assertNotEqual(self.run_tool('--account','work2','--session','new-session','env').returncode,0)
 def test_exec_inherits_beta_root_but_clears_token_override(self):
  self.login();bin=self.home/'bin';bin.mkdir();mock=bin/'claude'
  mock.write_text('#!/usr/bin/env python3\nimport os,json\nprint(json.dumps({k:os.getenv(k) for k in ["CLAUDE_CONFIG_DIR","CLAUDE_CODE_OAUTH_TOKEN"]}))\n');mock.chmod(0o755)
  self.env.update(PATH=str(bin)+':'+os.environ['PATH'],CLAUDE_CODE_OAUTH_TOKEN='wrong-account')
  r=self.run_tool('--account','work2','exec','--','-p','fixture');self.assertEqual(r.returncode,0,r.stderr)
  self.assertEqual(json.loads(r.stdout),{'CLAUDE_CONFIG_DIR':str(self.beta),'CLAUDE_CODE_OAUTH_TOKEN':None})
 def test_sync_is_repeatable_and_keeps_credentials_and_transcripts_separate(self):
  source=self.home/'.claude';source.mkdir()
  (source/'settings.json').write_text(json.dumps({'env':{'CLAUDE_CODE_OAUTH_TOKEN':'do-not-copy'},'model':'fixture'}))
  plugins=source/'plugins';plugins.mkdir();content=plugins/'cache/plugin';content.parent.mkdir();content.write_text('fixture');content.chmod(0o444)
  (plugins/'installed_plugins.json').write_text(json.dumps({'installPath':str(content)}))
  memory=source/'projects/project/memory';memory.mkdir(parents=True);(memory/'MEMORY.md').write_text('shared')
  (memory.parent/'old.jsonl').write_text('history')
  self.login()
  for _ in range(2):
   result=self.run_tool('--account','work2','sync');self.assertEqual(result.returncode,0,result.stderr)
  self.assertNotIn('CLAUDE_CODE_OAUTH_TOKEN',json.loads((self.beta/'settings.json').read_text())['env'])
  self.assertTrue((self.beta/'projects/project/memory').is_symlink())
  self.assertFalse((self.beta/'projects/project/old.jsonl').exists())
  self.assertEqual(json.loads((self.beta/'plugins/installed_plugins.json').read_text())['installPath'],str(self.beta/'plugins/cache/plugin'))
  self.assertEqual(json.loads((self.beta/'.credentials.json').read_text())['claudeAiOauth']['accessToken'],'fixture-only')
if __name__=='__main__':unittest.main()
