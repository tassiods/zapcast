#!/usr/bin/env python3
"""Build once, identify the prod binary, and hand that exact file to E2E/release."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def validate_bundle(bundle):
    if not (bundle / 'index.html').is_file() or not (bundle / 'sw.js').is_file():
        raise ValueError('build Vite production assets first')
    if not list((bundle / 'assets').glob('*.js')):
        raise ValueError('hashed production JavaScript is missing')


def source_dirty(cwd=None):
    return bool(subprocess.check_output(
        ['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=cwd, text=True).strip())


def build(version):
    if not re.fullmatch(r'(v\d+\.\d+\.\d+|ci)', version):
        raise ValueError('invalid version')
    if subprocess.check_output(['go', 'env', 'GOVERSION'], text=True).strip() != 'go1.26.9':
        raise ValueError('Go 1.26.9 required')
    if subprocess.check_output(['go', 'env', 'GOOS', 'GOARCH'], text=True).splitlines() != ['linux', 'amd64']:
        raise ValueError('this tested delivery target is linux/amd64 only')
    bundle = Path('internal/server/web/dist')
    validate_bundle(bundle)
    output = Path('release-artifacts')
    output.mkdir(exist_ok=True)
    if any(output.iterdir()):
        raise ValueError('release-artifacts must be empty; do not mix builds')
    binary = output / 'zapcast-linux-amd64'
    env = dict(os.environ, CGO_ENABLED='1', GOTOOLCHAIN='local')
    subprocess.run(['go', 'build', '-trimpath', '-tags', 'prod', '-ldflags',
                    '-s -w -X zapcast/internal/server.versionLdflag=' + version,
                    '-o', str(binary), '.'], check=True, env=env)
    checksum = hashlib.sha256(binary.read_bytes()).hexdigest()
    provenance = {
        'version': version,
        'sha': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(),
        'dirty': source_dirty(),
        'go': 'go1.26.9', 'tags': ['prod'], 'target': 'linux/amd64',
        'binary_sha256': checksum,
        'assets': {str(file.relative_to(bundle)): hashlib.sha256(file.read_bytes()).hexdigest()
                   for file in sorted(bundle.rglob('*')) if file.is_file()}}
    (output / 'provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
    (output / 'SHA256SUMS').write_text(checksum + '  zapcast-linux-amd64\n')
    print(json.dumps({k: v for k, v in provenance.items() if k != 'assets'}, indent=2))

if __name__ == '__main__': build(sys.argv[1])
