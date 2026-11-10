"""Local passthrough preserves arguments, working directory, stdin and exit code."""
import json,os,pathlib,subprocess,tempfile
binary=os.environ['FENNEC_TEST_BINARY']
with tempfile.TemporaryDirectory() as folder:
 root=pathlib.Path(folder);tools=root/'tools';tools.mkdir();work=root/'project with spaces';work.mkdir()
 executable=tools/'codex'
 executable.write_text('#!/usr/bin/python3\nimport os,sys,json\nprint(json.dumps({"args":sys.argv[1:],"cwd":os.getcwd(),"input":sys.stdin.read()}))\nsys.exit(23)\n')
 executable.chmod(0o700)
 result=subprocess.run([binary,'local-agent','codex','--model','literal $(not-executed)','line\nbreak'],input='stdin payload',text=True,capture_output=True,cwd=work,env={**os.environ,'HOME':folder,'PATH':str(tools)+':/usr/bin:/bin'})
 assert result.returncode==23,result.stderr
 data=json.loads(result.stdout)
 assert data=={'args':['--model','literal $(not-executed)','line\nbreak'],'cwd':str(work.resolve()),'input':'stdin payload'},data
 print('PASS local CLI passthrough argv/cwd/stdin/exit-code')
