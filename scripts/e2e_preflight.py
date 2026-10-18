#!/usr/bin/env python3
"""Never silently test go-run/dev embed instead of the supplied prod artifact."""
import os
from pathlib import Path
import subprocess


def check():
    value = os.environ.get('ZAPCAST_E2E_BINARY')
    if not value:
        raise ValueError('ZAPCAST_E2E_BINARY must explicitly name a built production binary')
    binary = Path(value).resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError('ZAPCAST_E2E_BINARY must be an executable file')
    info = subprocess.check_output(['go', 'version', '-m', str(binary)], text=True)
    if 'go1.26.9' not in info or '-tags=prod' not in info:
        raise ValueError('E2E requires Go 1.26.9 and the prod embed, not go run')
    # Frontend owner supplies a config which actually starts this exact binary.
    config = Path('e2e/run.mjs').read_text()
    if 'ZAPCAST_E2E_BINARY' not in config or 'spawn(binary' not in config or 'go run' in config:
        raise ValueError('Playwright production-binary config not integrated yet')
    print('Production E2E binary verified:', binary)

if __name__ == '__main__': check()
