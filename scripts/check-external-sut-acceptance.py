#!/usr/bin/env python3
"""Account for external-SUT evidence; never execute or simulate the protocol."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import runpy
import sys

ROOT = Path(__file__).resolve().parents[1]
PLAN = ROOT / 'docs/reference/testdata/external-sut-acceptance.json'
GUARD = runpy.run_path(str(ROOT / 'scripts/check-external-sut-vectors.py'))
need = GUARD['need']
decode = GUARD['decode']
TARGETS = ('go', 'java', 'paired')


def fields(value, names, label):
    need(isinstance(value, dict) and set(value) == set(names.split()), label + ': fields')


def nonempty(value):
    return isinstance(value, str) and bool(value.strip())


def load_plan():
    plan = decode(PLAN.read_bytes())
    fields(plan, 'format vector requirements', 'plan')
    need(type(plan['format']) is int and plan['format'] == 1, 'plan format')
    pin = plan['vector']
    fields(pin, 'path revision sha256', 'vector pin')
    need(pin['path'] == 'docs/reference/testdata/external-sut-v1.json', 'vector path')
    need(isinstance(pin['revision'], str) and re.fullmatch('[0-9a-f]{40}', pin['revision']), 'vector revision')
    raw = (ROOT / pin['path']).read_bytes()
    need(hashlib.sha256(raw).hexdigest() == pin['sha256'], 'vector digest differs from reviewed pin')
    data = decode(raw)
    GUARD['validate'](data)
    require_success(data)
    ids = set()
    need(isinstance(plan['requirements'], list) and plan['requirements'], 'requirements inventory')
    for requirement in plan['requirements']:
        fields(requirement, 'id targets owner description', 'requirement')
        name = requirement['id']
        need(nonempty(name) and name not in ids, 'duplicate/empty requirement ID')
        ids.add(name)
        targets = requirement['targets']
        need(isinstance(targets, list) and targets and all(t in TARGETS for t in targets)
             and len(set(targets)) == len(targets), name + ': targets')
        need(requirement['owner'] in (108, 109, 111), name + ': implementation owner')
        need(nonempty(requirement['description']), name + ': description')
    return plan, data


def require_success(data):
    # The old structural self-test assumes this family exists. Fail explicitly
    # here, without extending that guard into a reference protocol interpreter.
    need(any(c['id'] == 'success' and set(c['targets']) == {'go', 'java'}
             for c in data['cases']), 'missing required success case for Go and Java')


def inventory(plan, data, target):
    """Return ordered check IDs; repeated assertion names stay distinct."""
    need(target in TARGETS, 'unknown target')
    cases = {}
    for case in data['cases']:
        if target not in case['targets']:
            continue
        checks = []
        steps = GUARD['expand'](data, case['prefix']) + case['steps']
        for index, step in enumerate(steps):
            base = 'step/' + str(index)
            if step['peer'] == target:
                action = step['action']
                name = step['event'] if action == 'local' else step['frame']['type']
                checks.append(base + '/' + action + '/' + name)
                for occurrence, label in enumerate(step['expect']):
                    checks.append(base + '/expect/' + str(occurrence) + '/' + label)
            elif step.get('delivery') == 'exchange':
                # An exchange addressed to the scripted peer observes the
                # target's real write. It does not inject a second receipt.
                checks.append(base + '/output/' + step['frame']['type'])
        cases['case/' + case['id']] = checks
    for case in data['framing']:
        if target not in case['targets']:
            continue
        checks = ['input/input_hex']
        if 'control_hex' in case:
            checks.append('control/fresh_decoder_accepts')
        if 'read_chunk_sizes' in case:
            checks.append('input/read_chunk_sizes')
        if case.get('eof'):
            checks.append('input/eof')
        if 'decoded' in case:
            checks.append('observe/decoded')
        checks.extend('expect/' + str(i) + '/' + label for i, label in enumerate(case['expect']))
        cases['framing/' + case['id']] = checks
    for requirement in plan['requirements']:
        if target in requirement['targets']:
            cases['requirement/' + requirement['id']] = ['observe/evidence']
    return cases


def template(plan, data, target):
    return {
        'format': 1, 'target': target, 'vector': plan['vector'], 'run': None,
        'results': {name: {'status': 'incomplete', 'reason': 'No implementation evidence recorded.',
                           'checks': {}} for name in inventory(plan, data, target)},
    }


def validate_run(run, target, directory):
    fields(run, 'revision command repetitions repetition_method versions artifacts', 'run')
    need(isinstance(run['revision'], str) and re.fullmatch('[0-9a-f]{40}', run['revision']), 'implementation revision')
    need(nonempty(run['command']) and nonempty(run['repetition_method']), 'exact command/repetition method required')
    need(type(run['repetitions']) is int and run['repetitions'] >= 20, 'at least 20 repetitions required')
    if target == 'go':
        need(re.search(r'(?:^|\s)-count=' + str(run['repetitions']) + r'(?:\s|$)', run['command']),
             'Go command must record matching -count=N')
    versions = run['versions']
    needed = {'go'} if target == 'go' else {'java', 'spring', 'transaction_manager', 'jdbc_driver', 'pool', 'build_tool'}
    if target == 'paired':
        needed |= {'go', 'mysql'}
    need(isinstance(versions, dict) and needed <= set(versions)
         and all(nonempty(k) and nonempty(v) for k, v in versions.items()), 'missing concrete environment versions')
    artifacts = run['artifacts']
    need(isinstance(artifacts, dict) and artifacts, 'evidence artifacts required')
    for name, artifact in artifacts.items():
        need(nonempty(name), 'artifact ID')
        fields(artifact, 'path sha256', 'artifact ' + name)
        need(nonempty(artifact['path']), 'artifact path')
        path = Path(artifact['path'])
        need(not path.is_absolute() and '..' not in path.parts, 'artifact must be relative to manifest directory')
        resolved = (directory / path).resolve()
        need(resolved.is_relative_to(directory.resolve()), 'artifact escapes manifest directory')
        need(resolved.is_file(), 'missing evidence artifact: ' + str(path))
        need(hashlib.sha256(resolved.read_bytes()).hexdigest() == artifact['sha256'], 'evidence artifact digest: ' + name)
    return artifacts


def validate_result(result, plan, data, directory):
    fields(result, 'format target vector run results', 'manifest')
    need(type(result['format']) is int and result['format'] == 1, 'manifest format')
    need(result['vector'] == plan['vector'], 'manifest vector pin differs')
    expected = inventory(plan, data, result['target'])
    rows = result['results']
    need(isinstance(rows, dict), 'case results must be an object')
    missing, extra = set(expected) - set(rows), set(rows) - set(expected)
    need(not missing and not extra, 'case inventory: missing=' + ','.join(sorted(missing)) + ' unknown=' + ','.join(sorted(extra)))
    artifacts = validate_run(result['run'], result['target'], directory) if result['run'] is not None else {}
    counts = dict.fromkeys(('pass', 'fail', 'incomplete'), 0)
    for name, row in rows.items():
        fields(row, 'status reason checks', name)
        need(row['status'] in counts, name + ': unknown status (skip is not acceptance)')
        need(isinstance(row['reason'], str) and (row['status'] == 'pass' or nonempty(row['reason'])), name + ': reason required')
        checks = row['checks']
        need(isinstance(checks, dict) and set(checks) <= set(expected[name]), name + ': unknown check ID')
        for check_id, observation in checks.items():
            fields(observation, 'status handler evidence', name + '/' + check_id)
            need(observation['status'] in ('pass', 'fail'), name + ': observed status')
            need(nonempty(observation['handler']), name + ': real handler reference required')
            evidence = observation['evidence']
            need(isinstance(evidence, list) and evidence and all(nonempty(e) and e in artifacts for e in evidence),
                 name + ': observation requires recorded artifacts')
        if row['status'] == 'pass':
            need(set(checks) == set(expected[name]), name + ': unhandled checks')
            need(all(c['status'] == 'pass' for c in checks.values()), name + ': failing check cannot pass')
        elif row['status'] == 'fail':
            need(any(c['status'] == 'fail' for c in checks.values()), name + ': failure observation required')
        counts[row['status']] += 1
    return counts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument('--template', choices=TARGETS)
    action.add_argument('--inventory', choices=TARGETS)
    action.add_argument('--results', type=Path)
    parser.add_argument('--require-complete', action='store_true')
    args = parser.parse_args()
    if args.require_complete and args.results is None:
        parser.error('--require-complete needs --results')
    try:
        plan, data = load_plan()
        if args.template:
            print(json.dumps(template(plan, data, args.template), indent=2))
        elif args.inventory:
            print(json.dumps(inventory(plan, data, args.inventory), indent=2))
        else:
            result = decode(args.results.read_bytes())
            counts = validate_result(result, plan, data, args.results.parent)
            complete = counts['fail'] == counts['incomplete'] == 0
            print('EXTERNAL_SUT_ACCEPTANCE_RESULT target=' + result['target']
                  + ' manifest=valid acceptance=' + ('complete' if complete else 'incomplete')
                  + ''.join(' ' + key + '=' + str(value) for key, value in counts.items()))
            if args.require_complete and not complete:
                return 1
    except (ValueError, KeyError, TypeError, OSError) as err:
        print('external SUT acceptance: ' + str(err), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
