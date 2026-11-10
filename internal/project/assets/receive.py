"""Receive a manifest line followed by a tar stream into a fresh project snapshot."""
import hashlib
import json
import os
import pathlib
import re
import shutil
import sys
import tarfile
import tempfile

os.umask(0o077)
identifier = sys.argv[1]
if not re.fullmatch('[a-f0-9]{32}', identifier):
    raise SystemExit('invalid snapshot identity')
line = sys.stdin.buffer.readline(4 * 1024 * 1024 + 1)
if len(line) > 4 * 1024 * 1024 or not line.endswith(b'\n'):
    raise SystemExit('invalid manifest')
manifest = json.loads(line)
files = manifest['files']
if len(files) > 20000:
    raise SystemExit('too many files')

def safe(name):
    path = pathlib.PurePosixPath(name)
    return bool(name) and not path.is_absolute() and '..' not in path.parts and str(path) == name and name != '.'

expected = {}
total = 0
for entry in files:
    name, size = entry['path'], entry['size']
    if not safe(name) or name in expected or type(size) is not int or not 0 <= size <= 16 * 1024 * 1024:
        raise SystemExit('invalid file manifest')
    if not re.fullmatch('[0-9a-f]{64}', entry['sha256']):
        raise SystemExit('invalid digest')
    expected[name] = entry
    total += size
if total > 128 * 1024 * 1024 or total != manifest['bytes']:
    raise SystemExit('invalid total size')
workdir = manifest['working_directory']
if workdir != '.' and not safe(workdir):
    raise SystemExit('invalid working directory')
base = pathlib.Path.home() / '.local/fennec/projects'
base.mkdir(parents=True, exist_ok=True, mode=0o700)
target = base / identifier
stage = pathlib.Path(tempfile.mkdtemp(prefix='.receiving-', dir=base))
try:
    seen = set()
    with tarfile.open(fileobj=sys.stdin.buffer, mode='r|') as archive:
        for member in archive:
            entry = expected.get(member.name)
            if entry is None or member.name in seen or not member.isfile() or member.size != entry['size']:
                raise ValueError('unexpected archive member')
            destination = stage / member.name
            destination.parent.mkdir(parents=True, exist_ok=True)
            digest = hashlib.sha256()
            source = archive.extractfile(member)
            with open(destination, 'xb') as output:
                remaining = member.size
                while remaining:
                    chunk = source.read(min(remaining, 65536))
                    if not chunk:
                        raise ValueError('truncated member')
                    output.write(chunk)
                    digest.update(chunk)
                    remaining -= len(chunk)
            if digest.hexdigest() != entry['sha256']:
                raise ValueError('content mismatch')
            destination.chmod(0o700 if entry.get('executable') else 0o600)
            seen.add(member.name)
    if seen != set(expected):
        raise ValueError('missing archive files')
    (stage / workdir).mkdir(parents=True, exist_ok=True)
    if target.exists() or target.is_symlink():
        raise ValueError('snapshot already exists; do not overwrite cloud edits')
    # Serializes publication by snapshot identity across concurrent uploaders.
    import fcntl
    with open(base / (identifier + '.lock'), 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if target.exists() or target.is_symlink():
            raise ValueError('snapshot already exists')
        os.rename(stage, target)
    print(json.dumps({'directory': str(target / workdir), 'files': len(seen), 'bytes': total}))
finally:
    if stage.exists():
        shutil.rmtree(stage)
