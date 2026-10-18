#!/usr/bin/env python3
"""Validate an exact tested artifact offline; publication is explicit and gated."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request


def plan(version, sha, artifacts):
    if not re.fullmatch(r'(v\d+\.\d+\.\d+|ci)', version):
        raise ValueError('version must be vMAJOR.MINOR.PATCH (or ci for tests)')
    if not re.fullmatch(r'[a-f0-9]{40}', sha):
        raise ValueError('exact 40-character source SHA required')
    result = {'version': version, 'sha': sha, 'publication': False}
    if artifacts is not None:
        artifacts = Path(artifacts)
        provenance = json.loads((artifacts / 'provenance.json').read_text())
        if provenance.get('version') != version or provenance.get('sha') != sha:
            raise ValueError('artifact source/version does not match this gated run')
        if provenance.get('go') != 'go1.26.9' or provenance.get('tags') != ['prod']:
            raise ValueError('unexpected toolchain or embed')
        if version != 'ci' and provenance.get('dirty', True):
            raise ValueError('dirty source cannot be released')
        binary = artifacts / 'zapcast-linux-amd64'
        checksum = hashlib.sha256(binary.read_bytes()).hexdigest()
        if checksum != provenance['binary_sha256']:
            raise ValueError('tested binary checksum mismatch')
        sums = (artifacts / 'SHA256SUMS').read_text()
        if sums != checksum + '  zapcast-linux-amd64\n':
            raise ValueError('release checksums do not match the binary')
        if {p.name for p in artifacts.iterdir()} != {'zapcast-linux-amd64', 'provenance.json', 'SHA256SUMS'}:
            raise ValueError('unexpected files in release payload')
        result['binary_sha256'] = checksum
        result['assets'] = sorted(p.name for p in artifacts.iterdir())
    return result


def publish(result, artifacts, repo):
    if result['version'] == 'ci' or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repo):
        raise ValueError('publication needs a release version and owner/repo')
    if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('GITHUB_REF') != 'refs/heads/master' or os.environ.get('GITHUB_SHA') != result['sha']:
        raise ValueError('publication restricted to the gated master workflow')
    token = os.environ['GITHUB_TOKEN']
    def api(url, method='GET', data=None, content_type='application/json'):
        if isinstance(data, dict): data = json.dumps(data).encode()
        req = urllib.request.Request(url, data=data, method=method, headers={
            'Authorization': 'Bearer ' + token, 'Accept': 'application/vnd.github+json',
            'Content-Type': content_type, 'X-GitHub-Api-Version': '2022-11-28'})
        with urllib.request.urlopen(req, timeout=60) as response:
            return json.load(response)
    base = 'https://api.github.com/repos/' + repo
    # Never overwrite an existing version, even if its artifacts are missing.
    for endpoint in ['/git/ref/tags/' + result['version'], '/releases/tags/' + result['version']]:
        try: api(base + endpoint)
        except urllib.error.HTTPError as err:
            if err.code != 404: raise
        else: raise ValueError('version already exists; choose a new reviewed version')
    release = api(base + '/releases', 'POST', {
        'tag_name': result['version'], 'target_commitish': result['sha'],
        'name': 'ZapCast ' + result['version'], 'draft': True, 'prerelease': False,
        'body': 'Tested source: ' + result['sha'] + '\nGo 1.26.9; production embed. Verify SHA256SUMS before use.'})
    upload = release['upload_url'].split('{')[0]
    for name in result['assets']:
        file = Path(artifacts) / name
        api(upload + '?name=' + urllib.parse.quote(name), 'POST', file.read_bytes(), 'application/octet-stream')
    target = base + '/releases/' + str(release['id'])
    checked = api(target)
    expected = {name: (Path(artifacts) / name).stat().st_size for name in result['assets']}
    if {a['name']: a['size'] for a in checked['assets']} != expected:
        raise ValueError('uploaded draft asset mismatch; left draft for investigation')
    hashes = {name: 'sha256:' + hashlib.sha256((Path(artifacts) / name).read_bytes()).hexdigest()
              for name in result['assets']}
    if {a['name']: a.get('digest') for a in checked['assets']} != hashes:
        raise ValueError('uploaded draft digest mismatch; left draft for investigation')
    api(target, 'PATCH', {'draft': False})
    checked = api(target)
    ref = api(base + '/git/ref/tags/' + result['version'])
    obj = ref['object']
    if obj['type'] == 'tag': obj = api(base + '/git/tags/' + obj['sha'])['object']
    if checked['draft'] or obj['sha'] != result['sha']:
        raise ValueError('publication read-back failed')
    print('Published exact tested SHA:', result['sha'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--dry-run', action='store_true')
    mode.add_argument('--publish', action='store_true')
    parser.add_argument('--version', default='v0.0.0')
    parser.add_argument('--sha')
    parser.add_argument('--artifacts', type=Path)
    parser.add_argument('--repo')
    args = parser.parse_args()
    sha = args.sha or subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    result = plan(args.version, sha, args.artifacts)
    if args.publish:
        if args.artifacts is None or args.repo is None: parser.error('publication requires artifacts and repo')
        publish(result, args.artifacts, args.repo)
    else:
        print(json.dumps(result, indent=2))

if __name__ == '__main__': main()
