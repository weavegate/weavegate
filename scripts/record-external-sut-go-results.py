#!/usr/bin/env python3
"""Convert repeated Go observer records into the shared acceptance manifest.

This tool only accounts for emitted checks. Missing runtime handlers remain
incomplete, including all checks whose labels have not been observed.
"""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import runpy
import sys

ROOT = Path(__file__).resolve().parents[1]
ACCOUNTING = runpy.run_path(str(ROOT / 'scripts/check-external-sut-acceptance.py'))


def source_symbols(source):
    # Only named functions and receiver methods are valid observer references.
    # Erase Go comments and literals before looking for declarations, so a
    # commented-out function or a quoted example cannot validate stale evidence.
    code = re.sub(r'//[^\n]*|/\*[\s\S]*?\*/|`[^`]*`|"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'',
                  lambda match: re.sub(r'[^\n]', ' ', match.group()), source.read_text())
    declarations = re.findall(
        r'^\s*func\s+(?:\(\s*(?:\w+\s+)?\*?(\w+)\s*\)\s*)?(\w+)\s*\(', code, re.MULTILINE)
    return {receiver + '.' + name if receiver else name for receiver, name in declarations}


def record(log, revision, command, repetitions, version):
    plan, data = ACCOUNTING['load_plan']()
    inventory = ACCOUNTING['inventory'](plan, data, 'go')
    result = ACCOUNTING['template'](plan, data, 'go')
    raw = log.read_bytes()
    text = raw.decode('utf-8')
    if repetitions < 20 or not re.search(r'(?:^|\s)-count=' + str(repetitions) + r'(?:\s|$)', command):
        raise ValueError('at least 20 matching repetitions required')
    if not re.fullmatch('[0-9a-f]{40}', revision):
        raise ValueError('full implementation revision required')
    if re.search(r'^\s*(?:FAIL|--- FAIL:|WARNING: DATA RACE)', text, re.MULTILINE):
        raise ValueError('test log contains failures')
    if not re.search(r'^ok\s+github.com/weavegate/weavegate/internal/sut/external\s', text, re.MULTILINE):
        raise ValueError('successful external package result missing')
    seen = {}
    handlers = {}
    symbols = {}
    owners = {}
    runs, passed = Counter(), Counter()
    active = None
    marker = 'EXTERNAL_SUT_CHECK '
    for line in text.splitlines():
        if line.startswith(('=== PAUSE', '=== CONT')):
            raise ValueError('parallel test logs are not supported')
        start = re.fullmatch(r'=== RUN\s+(\w+)', line)
        finish = re.fullmatch(r'--- (PASS|SKIP): (\w+) \([^)]*\)', line)
        if start:
            if active is not None:
                raise ValueError('overlapping test repetitions')
            active = start[1]
            runs[active] += 1
        if finish:
            if active != finish[2]:
                raise ValueError('unmatched test completion')
            if finish[1] == 'PASS':
                passed[active] += 1
            active = None
        if marker not in line:
            continue
        if active is None:
            raise ValueError('observer outside a test repetition')
        entry = ACCOUNTING['decode'](line.split(marker, 1)[1].encode())
        ACCOUNTING['fields'](entry, 'row check handler', 'observer record')
        row, check, handler = entry['row'], entry['check'], entry['handler']
        if row not in inventory or check not in inventory[row]:
            raise ValueError('unknown observer check: ' + row + '/' + check)
        if not isinstance(handler, str) or not handler.startswith('internal/sut/external/'):
            raise ValueError('invalid observer source reference')
        parts = handler.split(':')
        if len(parts) != 2 or not re.fullmatch(r'\w+(?:\.\w+)?', parts[1]):
            raise ValueError('invalid observer symbol reference')
        source = (ROOT / parts[0]).resolve()
        if not source.is_relative_to(ROOT / 'internal/sut/external') or not source.is_file():
            raise ValueError('missing observer source')
        if source not in symbols:
            symbols[source] = source_symbols(source)
        if parts[1] not in symbols[source]:
            raise ValueError('missing observer symbol: ' + handler)
        key = (row, check)
        if key in handlers and handlers[key] != handler:
            raise ValueError('observer changed between repetitions')
        handlers[key] = handler
        if key in owners and owners[key] != active:
            raise ValueError('observer changed owning test')
        owners[key] = active
        occurrence = runs[active]
        observed = seen.setdefault(key, set())
        if occurrence in observed:
            raise ValueError('duplicate observer within a repetition')
        observed.add(occurrence)
    expected = set(range(1, repetitions + 1))
    if active is not None or not seen or any(observed != expected for observed in seen.values()):
        raise ValueError('each observed check must occur once per repetition')
    if any(runs[owner] != repetitions or passed[owner] != repetitions for owner in owners.values()):
        raise ValueError('each observer test must pass every repetition')
    for (row, check), handler in handlers.items():
        result['results'][row]['checks'][check] = {
            'status': 'pass', 'handler': handler, 'evidence': ['go-log'],
        }
    for row, expected in inventory.items():
        item = result['results'][row]
        missing = set(expected) - set(item['checks'])
        if missing:
            item['reason'] = str(len(missing)) + ' checks lack executable observer evidence; acceptance remains incomplete.'
        else:
            item['status'], item['reason'] = 'pass', ''
    result['run'] = {
        'revision': revision, 'command': command, 'repetitions': repetitions,
        'repetition_method': 'Go test -count repeats each test, with one record per observed check per repetition.',
        'versions': {'go': version},
        'artifacts': {'go-log': {'path': log.name, 'sha256': hashlib.sha256(raw).hexdigest()}},
    }
    ACCOUNTING['validate_result'](result, plan, data, log.parent)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--log', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--command', required=True)
    parser.add_argument('--repetitions', type=int, default=20)
    parser.add_argument('--go-version', required=True)
    args = parser.parse_args()
    try:
        if args.log.resolve().parent != args.output.resolve().parent or args.log.resolve() == args.output.resolve():
            raise ValueError('manifest and distinct log must share a directory')
        result = record(args.log, args.revision, args.command, args.repetitions, args.go_version)
        args.output.write_text(json.dumps(result, indent=2) + '\n')
    except (ValueError, OSError, KeyError, TypeError) as err:
        print('EXTERNAL_SUT_GO_RECORD_ERROR ' + str(err), file=sys.stderr)
        return 1
    print('EXTERNAL_SUT_GO_RECORD_RESULT observations=recorded unobserved=incomplete')
    return 0


if __name__ == '__main__':
    sys.exit(main())
