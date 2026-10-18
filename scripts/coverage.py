#!/usr/bin/env python3
import re
import sys
from pathlib import Path


def check(text, minimum):
    match = re.search(r'^total:\s+\(statements\)\s+([0-9]+(?:\.[0-9]+)?)%$', text, re.MULTILINE)
    if not match:
        raise ValueError('coverage total missing or malformed')
    actual = float(match.group(1))
    if not 0 <= actual <= 100 or not 0 <= minimum <= 100:
        raise ValueError('invalid coverage percentage')
    print(f'Coverage {actual}% (minimum {minimum}%)')
    return actual >= minimum

if __name__ == '__main__':
    sys.exit(0 if check(Path(sys.argv[1]).read_text(), float(sys.argv[2])) else 1)
