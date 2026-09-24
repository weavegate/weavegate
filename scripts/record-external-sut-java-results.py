#!/usr/bin/env python3
"""Convert repeated Java observer records into the shared acceptance manifest.

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
SOURCES = ROOT / 'sdk/java/src/test/java'
VERSION_KEYS = ('java', 'spring', 'transaction_manager', 'jdbc_driver', 'pool', 'build_tool')


def source_symbols(source):
    # Erase comments, text blocks, strings and character literals before looking
    # for declarations, so quoted or commented examples cannot validate evidence.
    code = re.sub(r'//[^\n]*|/\*[\s\S]*?\*/|"""[\s\S]*?"""|"(?:\\.|[^"\\\n])*"|\'(?:\\.|[^\'\\\n])*\'',
                  lambda match: re.sub(r'[^\n]', ' ', match.group()), source.read_text())
    types = {match.end() - 1: match[1] for match in re.finditer(
        r'\b(?:class|record|interface|enum)\s+(\w+)[^;{}]*\{', code)}
    methods = {match.end() - 1: match[1] for match in re.finditer(
        r'^\s*(?:@\w+(?:\([^)]*\))?\s*)*(?:(?:public|protected|private|static|final|synchronized|abstract|default)\s+)*'
        r'(?:<[^>]+>\s+)?[\w.<>\[\],?]+(?:\s*<[^>]*>)?\s+(\w+)\s*\([^;{}]*\)\s*(?:throws\s+[\w.,\s]+)?\{',
        code, re.MULTILINE)}
    symbols, scopes = set(), []
    for brace in re.finditer(r'[{}]', code):
        if brace[0] == '}':
            if not scopes:
                raise ValueError('unbalanced observer source')
            scopes.pop()
            continue
        position = brace.start()
        if position in types and (not scopes or scopes[-1] is not None):
            parent = scopes[-1] + '.' if scopes else ''
            scopes.append(parent + types[position])
        else:
            if position in methods and scopes and scopes[-1] is not None:
                symbols.add(scopes[-1] + '.' + methods[position])
            # Method bodies, initializers and anonymous types are not named
            # declaring types. Do not attribute their contents to an outer type.
            scopes.append(None)
    if scopes:
        raise ValueError('unbalanced observer source')
    return symbols


def decode(line, marker):
    entry = ACCOUNTING['decode'](line.split(marker, 1)[1].encode())
    return entry


def record(log, build_log, revision, command, repetitions):
    plan, data = ACCOUNTING['load_plan']()
    inventory = ACCOUNTING['inventory'](plan, data, 'java')
    result = ACCOUNTING['template'](plan, data, 'java')
    raw = log.read_bytes()
    build = build_log.read_bytes()
    text = raw.decode('utf-8')
    if repetitions < 20 or not re.search(r'(?:^|\s)-Dweavegate\.repetitions=' + str(repetitions) + r'(?:\s|$)', command):
        raise ValueError('at least 20 matching repetitions required')
    if not re.fullmatch('[0-9a-f]{40}', revision):
        raise ValueError('full implementation revision required')
    build_text = build.decode('utf-8', 'replace')
    if not re.search(r'^\[INFO\] BUILD SUCCESS$', build_text, re.MULTILINE) or re.search(r'^\[ERROR\]', build_text, re.MULTILINE):
        raise ValueError('successful Maven build result missing')
    versions = None
    handlers, owners, symbols = {}, {}, {}
    seen = {}
    runs, passed = Counter(), Counter()
    repetitions_by_test = {}
    active = None
    for line in text.splitlines():
        if line.startswith('EXTERNAL_SUT_VERSIONS '):
            if versions is not None:
                raise ValueError('duplicate version record')
            versions = decode(line, 'EXTERNAL_SUT_VERSIONS ')
            if not isinstance(versions, dict) or set(versions) != set(VERSION_KEYS) or not all(
                    isinstance(v, str) and v.strip() and 'null' not in v for v in versions.values()):
                raise ValueError('incomplete version record')
            continue
        event = re.match(r'EXTERNAL_SUT_TEST_(RUN|PASS|FAIL|SKIP) ', line)
        if event:
            entry = decode(line, event.group(0))
            ACCOUNTING['fields'](entry, 'test repetition', 'test record')
            test, repetition = entry['test'], entry['repetition']
            if not isinstance(test, str) or not test or type(repetition) is not int or repetition < 1:
                raise ValueError('invalid test record')
            kind = event.group(1)
            if kind in ('FAIL', 'SKIP'):
                raise ValueError('test log contains failed or skipped tests')
            if kind == 'RUN':
                if active is not None:
                    raise ValueError('overlapping test repetitions')
                active = (test, repetition)
                runs[test] += 1
                observed = repetitions_by_test.setdefault(test, set())
                if repetition in observed:
                    raise ValueError('duplicate test repetition')
                observed.add(repetition)
            else:
                if active != (test, repetition):
                    raise ValueError('unmatched test completion')
                passed[test] += 1
                active = None
            continue
        if line.startswith('EXTERNAL_SUT_CHECK '):
            if active is None:
                raise ValueError('observer outside a test repetition')
            entry = decode(line, 'EXTERNAL_SUT_CHECK ')
            ACCOUNTING['fields'](entry, 'row check handler', 'observer record')
            row, check, handler = entry['row'], entry['check'], entry['handler']
            if row not in inventory or check not in inventory[row]:
                raise ValueError('unknown observer check: ' + str(row) + '/' + str(check))
            if not isinstance(handler, str) or not handler.startswith('sdk/java/src/test/java/'):
                raise ValueError('invalid observer source reference')
            parts = handler.split(':')
            if len(parts) != 2 or not re.fullmatch(r'\w+(?:\.\w+)+', parts[1]):
                raise ValueError('invalid observer symbol reference')
            source = (ROOT / parts[0]).resolve()
            if not source.is_relative_to(SOURCES) or not source.is_file() or source.suffix != '.java':
                raise ValueError('missing observer source')
            if source not in symbols:
                symbols[source] = source_symbols(source)
            if parts[1] not in symbols[source]:
                raise ValueError('missing observer symbol: ' + handler)
            key = (row, check)
            if handlers.setdefault(key, handler) != handler:
                raise ValueError('observer changed between repetitions')
            if owners.setdefault(key, active[0]) != active[0]:
                raise ValueError('observer changed owning test')
            observed = seen.setdefault(key, set())
            if active[1] in observed:
                raise ValueError('duplicate observer within a repetition')
            observed.add(active[1])
    if active is not None or versions is None or not seen:
        raise ValueError('incomplete evidence log')
    expected = set(range(1, repetitions + 1))
    if any(observed != expected for observed in seen.values()):
        raise ValueError('each observed check must occur once per repetition')
    for owner in set(owners.values()):
        if runs[owner] != repetitions or passed[owner] != repetitions or repetitions_by_test[owner] != expected:
            raise ValueError('each observer test must pass every repetition')
    for (row, check), handler in handlers.items():
        result['results'][row]['checks'][check] = {
            'status': 'pass', 'handler': handler, 'evidence': ['java-log', 'java-build-log'],
        }
    for row, needed in inventory.items():
        item = result['results'][row]
        missing = set(needed) - set(item['checks'])
        if missing:
            item['reason'] = str(len(missing)) + ' checks lack executable observer evidence; acceptance remains incomplete.'
        else:
            item['status'], item['reason'] = 'pass', ''
    result['run'] = {
        'revision': revision, 'command': command, 'repetitions': repetitions,
        'repetition_method': 'JUnit dynamic tests generate one execution per repetition from -Dweavegate.repetitions; '
                             'the launcher listener records each execution boundary and result.',
        'versions': versions,
        'artifacts': {
            'java-log': {'path': log.name, 'sha256': hashlib.sha256(raw).hexdigest()},
            'java-build-log': {'path': build_log.name, 'sha256': hashlib.sha256(build).hexdigest()},
        },
    }
    ACCOUNTING['validate_result'](result, plan, data, log.parent)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--log', type=Path, required=True)
    parser.add_argument('--build-log', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--command', required=True)
    parser.add_argument('--repetitions', type=int, default=20)
    args = parser.parse_args()
    try:
        paths = [args.log.resolve(), args.build_log.resolve(), args.output.resolve()]
        if len({p.parent for p in paths}) != 1 or len(set(paths)) != 3:
            raise ValueError('manifest and distinct logs must share a directory')
        result = record(args.log, args.build_log, args.revision, args.command, args.repetitions)
        args.output.write_text(json.dumps(result, indent=2) + '\n')
    except (ValueError, OSError, KeyError, TypeError) as err:
        print('EXTERNAL_SUT_JAVA_RECORD_ERROR ' + str(err), file=sys.stderr)
        return 1
    print('EXTERNAL_SUT_JAVA_RECORD_RESULT observations=recorded unobserved=incomplete')
    return 0


if __name__ == '__main__':
    sys.exit(main())
