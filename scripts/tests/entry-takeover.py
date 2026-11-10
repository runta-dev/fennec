import pathlib,tempfile,subprocess,os,json
binary=os.environ['FENNEC_TEST_BINARY']
with tempfile.TemporaryDirectory() as d:
 root=pathlib.Path(d);tools=root/'bin';tools.mkdir();real=root/'real'
 real.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo 2.1.318; exit 0; fi\necho "ORIGINAL:$1"\nexit 23\n');real.chmod(0o700)
 entry=tools/'claude';entry.symlink_to(real)
 env={**os.environ,'HOME':d,'PATH':str(tools)+':/usr/bin:/bin'}
 shell_name=os.environ.get('FENNEC_TEST_SHELL','bash')
 shell_args={'zsh':['zsh','-f'],'fish':['fish','--no-config','-c','while read -l line; eval $line; end'],'bash':['bash','--noprofile','--norc']}[shell_name]
 status='$status' if shell_name=='fish' else '$?'
 prime='command -s claude >/dev/null' if shell_name=='fish' else 'hash claude'
 shell=subprocess.Popen(shell_args,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,env=env)
 try:
  shell.stdin.write(prime+'; echo CACHED\n');shell.stdin.flush();assert shell.stdout.readline().strip()=='CACHED'
  subprocess.run([binary,'install-entry','claude',str(entry)],env=env,check=True)
  shell.stdin.write('claude literal; echo EXIT:'+status+'\n');shell.stdin.flush()
  line=shell.stdout.readline().strip();assert line=='ORIGINAL:literal',line
  line=shell.stdout.readline().strip();assert line=='EXIT:23',line
  installed=subprocess.run([binary,'install-shims'],env=env,capture_output=True,text=True,check=True)
  shims=json.loads(installed.stdout)['directory']
  shimenv={**env,'PATH':shims+':'+env['PATH']}
  local=subprocess.run(['claude','via-shim'],env=shimenv,capture_output=True,text=True,timeout=5)
  assert local.returncode==23 and local.stdout.strip()=='ORIGINAL:via-shim',(local.returncode,local.stdout,local.stderr)
  versions=subprocess.run([binary,'agent-versions'],env=shimenv,capture_output=True,text=True,check=True,timeout=30)
  claude=next(x for x in json.loads(versions.stdout) if x['id']=='claude')
  assert claude['version']=='2.1.318' and pathlib.Path(claude['path']).resolve()==real.resolve(),claude
  cfg=root/'.config/fennec';cfg.mkdir(parents=True);(cfg/'takeover.json').write_text(json.dumps({'agents':['claude']}))
  version=subprocess.run(['claude','--version'],env=shimenv,capture_output=True,text=True,timeout=5)
  assert version.returncode==0 and version.stdout.strip()=='2.1.318',version.stderr
  shell.stdin.write('claude remote; echo DONE:'+status+'\n');shell.stdin.flush()
  lines=[]
  for _ in range(8):
   line=shell.stdout.readline().strip();lines.append(line)
   if line.startswith('DONE:'):break
  assert any('broker unavailable' in line for line in lines),lines
  assert lines[-1]=='DONE:1',lines
  subprocess.run([binary,'restore-entry',str(entry)],env=env,check=True)
  assert entry.is_symlink()
  print('PASS cached shell: passthrough, cloud dispatch without source, restore')
 finally:
  shell.stdin.close();shell.wait(timeout=5)
