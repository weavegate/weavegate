#!/usr/bin/env python3
"""Lint constructed external-SUT histories; this is not a protocol implementation."""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import re
import sys

DEFAULT = Path(__file__).resolve().parents[1] / 'docs/reference/testdata/external-sut-v1.json'
BODY = {
    'start': {'variant', 'params', 'commands', 'points', 'capacity', 'database', 'startup_ms', 'cancel_ms'},
    'ready': {'commands', 'points', 'capacity'},
    'invoke': {'invocation', 'worker', 'command'},
    'accepted': {'invocation', 'worker'},
    'arrive': {'invocation', 'worker', 'arrival', 'point'},
    'release': {'invocation', 'worker', 'arrival', 'point'},
    'terminal': {'invocation', 'worker', 'transaction', 'connection', 'error'},
    'cancel': {'invocation', 'worker', 'reason'},
    'stop': {'budget_ms'}, 'stopped': set(), 'fatal': {'kind', 'message'},
}
TO_JAVA = {'start', 'invoke', 'release', 'cancel', 'stop'}
EVENTS = set('''advance_cancel_cleanup_clock advance_fatal_cleanup_clock advance_startup_clock
advance_stop_clock application_cleanup_complete begin_evaluation cancel_context
check_operation_result check_stop_results child_exit command_exception complete_evaluation
completion hold_cleanup hold_release_enqueue invoke_call jdbc_blocked launch_child
provisional_evaluation readiness_complete resume_release_enqueue runtime_arrive_returns
startup_before_write startup_deadline stderr_bytes stop_call stop_deadline stop_half_deadline
wait_arrive_timeout worker_arrives'''.split())
REQUIRED = set('''duplicate_go duplicate_java late_process_fault late_wire_fault
startup_watchdog stop_watchdog cancel_watchdog fatal_watchdog active_stop_watchdog
cancel_wins_enqueue enqueue_wins_cancel readiness_rejection canceled_reuse
normal_active_stop exception_input'''.split())


def need(condition, message):
    if not condition:
        raise ValueError(message)


def unique(pairs):
    result = {}
    for key, value in pairs:
        need(key not in result, 'duplicate JSON key: ' + key)
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw.decode('utf-8'), object_pairs_hook=unique)


def integer(value, low, high):
    return type(value) is int and low <= value <= high


def frame_shape(f):
    need(isinstance(f, dict) and set(f) == {'v', 'type', 'run', 'session', 'seq', 'body'}, 'frame envelope')
    need(integer(f['v'], 1, 2147483647) and integer(f['seq'], 1, 100000), 'version/sequence type or range')
    for key in ('run', 'session'):
        need(isinstance(f[key], str) and re.fullmatch('[a-f0-9]{32}', f[key]), key + ' identity')
    t, b = f['type'], f['body']
    need(t in BODY and isinstance(b, dict) and set(b) == BODY[t], 'message body fields')
    if 'invocation' in b:
        need(isinstance(b['invocation'], str) and re.fullmatch('[a-f0-9]{32}', b['invocation']), 'invocation identity')
        need(isinstance(b['worker'], str) and b['worker'].strip() == b['worker'] != '', 'worker name')
    if 'arrival' in b:
        need(isinstance(b['arrival'], str) and re.fullmatch('[1-9][0-9]*', b['arrival']) and int(b['arrival']) <= 100000, 'arrival identity')
    for key in ('startup_ms', 'cancel_ms', 'budget_ms'):
        if key in b:
            need(integer(b[key], 1, 2147483647), 'invalid ' + key)
    if t in ('start', 'ready'):
        need(integer(b['capacity'], 1, 1024), 'capacity')
        for key in ('commands', 'points'):
            a = b[key]
            need(isinstance(a, list) and all(isinstance(v, str) and v for v in a) and len(a) == len(set(a)), key)
    if t == 'start':
        db = b['database']
        need(isinstance(db, dict) and set(db) == {'driver', 'host', 'port', 'name', 'username', 'password'}, 'database fields')
        need(db['driver'] == 'mysql' and integer(db['port'], 1, 65535), 'database driver/port')
        need(isinstance(b['params'], dict) and all(isinstance(k, str) and isinstance(v, str) for k, v in b['params'].items()), 'params')
    if t == 'cancel':
        need(b['reason'] in ('context', 'stop'), 'cancel reason')
    if t == 'terminal':
        need(b['transaction'] in ('committed', 'rolled_back', 'not_started'), 'transaction outcome')
        need(b['connection'] in ('returned', 'not_acquired'), 'lease outcome')
        need(b['connection'] != 'not_acquired' or b['transaction'] == 'not_started', 'unacquired transaction')
        err = b['error']
        if err is None:
            need(b['transaction'] == 'committed' and b['connection'] == 'returned', 'nil terminal error')
        else:
            need(isinstance(err, dict) and set(err) == {'kind', 'message', 'mysql_code', 'sql_state'}, 'error fields')
            need(err['kind'] in ('application', 'mysql', 'cancelled') and isinstance(err['message'], str), 'error kind/message')
            need(integer(err['mysql_code'], 0, 65535), 'MySQL code')
            need((err['mysql_code'] > 0 and isinstance(err['sql_state'], str) and re.fullmatch('[A-Z0-9]{5}', err['sql_state'])) if err['kind'] == 'mysql' else (err['mysql_code'] == 0 and err['sql_state'] == ''), 'SQL error metadata')


def expand(data, name, seen=()):
    need(name in data['prefixes'] and name not in seen, 'missing/cyclic prefix: ' + name)
    out = []
    for step in data['prefixes'][name]:
        out.extend(expand(data, step['prefix'], seen + (name,)) if set(step) == {'prefix'} else [step])
        need(len(out) <= 10000, 'prefix expansion too large')
    return out


def event(step, name, peer=None):
    return step.get('event') == name and (peer is None or step['peer'] == peer)


def message(step, kind, peer=None):
    return step.get('frame', {}).get('type') == kind and (peer is None or step['peer'] == peer)


def indices(steps, predicate):
    return [i for i, s in enumerate(steps) if predicate(s)]


def history(case, steps):
    targets = case['targets']
    need(case['execution'] == 'isolated' and targets and len(targets) == len(set(targets)) and set(targets) <= {'go', 'java'}, 'case scope')
    seq = {'go': {}, 'java': {}}
    invocations, active, sources, reasons = {}, {}, {}, {}
    binding = None
    start = None
    for step in steps:
        need(step.get('peer') in seq and isinstance(step.get('expect'), list) and step['expect'] and all(isinstance(v, str) for v in step['expect']), 'step receiver/effects')
        effects = step['expect']
        if step.get('action') == 'local':
            need(set(step) == {'peer', 'action', 'event', 'args', 'expect'} and step['event'] in EVENTS and isinstance(step['args'], dict), 'local event shape/vocabulary')
            a = step['args']
            if event(step, 'stop_call'):
                need(integer(a.get('budget_ms'), 1, 2147483647), 'local Stop budget')
                need(effects[0] == 'set_single_deadline' if 'set_single_deadline' in effects else bool({'reuse_deadline', 'no_new_deadline'} & set(effects)), 'Stop deadline must precede writes')
            if event(step, 'invoke_call', 'go'):
                iid = a['invocation']
                need(iid not in invocations and a['worker'] not in active, 'duplicate/unretired Go reservation')
                invocations[iid] = (a['worker'], a['command'])
                active[a['worker']] = iid
            if event(step, 'command_exception', 'java'):
                sources[a['invocation']] = a['exception']
            if event(step, 'stderr_bytes'):
                raw = b''.join(bytes.fromhex(v['hex']) * v['repeat'] for v in a['segments'])
                need(len(raw) == a['byte_count'] and 0 < a['retained_bytes'] < len(raw), 'stderr input size')
                need(hashlib.sha256(raw[-a['retained_bytes']:]).hexdigest() == a['retained_sha256'], 'stderr retained content')
            continue
        need(step.get('action') == 'receive' and set(step) == {'peer', 'action', 'frame', 'expect', 'delivery'} and step['delivery'] in ('input', 'exchange'), 'receive shape/delivery')
        f = step['frame']
        frame_shape(f)
        peer, t, b = step['peer'], f['type'], f['body']
        need(step['delivery'] != 'input' or peer in targets, 'injected input has no target receiver')
        need(t == 'fatal' or (peer == 'java') == (t in TO_JAVA), 'message direction')
        if f['v'] != 1:
            need(step['delivery'] == 'input' and 'fatal_version' in effects, 'unmarked incompatible version')
            continue
        if binding is None:
            binding = (f['run'], f['session'])
        if binding != (f['run'], f['session']):
            need(step['delivery'] == 'input' and 'drop_foreign_session' in effects, 'foreign session must be injected and dropped')
            continue
        seen = seq[peer]
        payload = json.dumps(f, separators=(',', ':'), ensure_ascii=False)
        if f['seq'] in seen or f['seq'] != len(seen) + 1:
            if seen.get(f['seq']) == payload:
                need(step['delivery'] == 'input' and {'ignore_duplicate', 'no_reply'} <= set(effects), 'exact duplicate effects')
            else:
                need(step['delivery'] == 'input' and 'fatal_protocol' in effects, 'unmarked sequence conflict/gap')
            continue
        seen[f['seq']] = payload
        if t == 'start':
            start = b
        if t == 'ready' and start:
            matches = all(b[k] == start[k] for k in ('commands', 'points', 'capacity'))
            if not matches:
                need(targets == ['go'] and step['delivery'] == 'input' and 'startup_error' in effects, 'mismatched ready must be Go-only input')
        if step['delivery'] == 'input':
            continue
        if t == 'invoke':
            need(invocations.get(b['invocation']) == (b['worker'], b['command']), 'invoke lacks Go Handle call')
        if t == 'accepted':
            need(b['invocation'] in invocations, 'accepted lacks reservation')
        if t == 'cancel':
            reasons.setdefault(b['invocation'], b['reason'])
            if 'request_jdbc_cancel' in effects and 'java' in targets:
                # An already-canceled invocation does not need another watchdog.
                need('cancel_latched' not in effects or ('arm_cleanup_watchdog' in effects and effects.index('arm_cleanup_watchdog') < effects.index('request_jdbc_cancel')), 'Java cancellation watchdog must precede JDBC cancel')
        if t == 'terminal':
            iid, err = b['invocation'], b['error']
            need(iid in invocations, 'terminal lacks invocation')
            if err and 'java' in targets:
                if err['kind'] == 'cancelled':
                    need(iid in reasons and err['message'] == 'cancelled by ' + reasons[iid], 'cancellation message/source')
                else:
                    source = sources.get(iid)
                    need(source is not None and (source['message'], source['vendor_code'], source['sql_state']) == (err['message'], err['mysql_code'], err['sql_state']), 'terminal exception lacks independent source')
            if b['transaction'] == 'not_started':
                need('no_worker_result' in effects, 'unstarted WorkerResult contradicts G5')
            if 'close_result_channel' in effects:
                active.pop(b['worker'], None)


def coverage(rule, case, steps):
    targets = case['targets']
    own = case['steps']
    if rule.startswith('duplicate_'):
        peer = rule.removeprefix('duplicate_')
        duplicates = [s for s in own if s.get('delivery') == 'input' and s['peer'] == peer and 'ignore_duplicate' in s['expect']]
        return peer in targets and bool(duplicates) and all('no_reply' in s['expect'] and ('no_redispatch' if peer == 'java' else 'no_client_arrive') in s['expect'] for s in duplicates)
    if rule.startswith('late_'):
        before = indices(steps, lambda s: message(s, 'terminal', 'go'))
        provisional = indices(steps, lambda s: event(s, 'provisional_evaluation'))
        fault = indices(steps, lambda s: message(s, 'fatal', 'go') if rule == 'late_wire_fault' else event(s, 'child_exit', 'go'))
        completion = indices(steps, lambda s: event(s, 'complete_evaluation'))
        return 'go' in targets and bool(before and provisional and fault and completion) and max(before) < provisional[0] < fault[0] < completion[0] and 'invalidate_evaluation' in steps[fault[0]]['expect'] and steps[-1].get('event') == 'check_operation_result' and steps[-1]['args']['expected_error'] in ('transport_failure', 'adapter_failure')
    if rule.endswith('watchdog'):
        phase = rule.removesuffix('_watchdog')
        clock = 'advance_' + {'startup': 'startup', 'stop': 'stop', 'cancel': 'cancel_cleanup', 'fatal': 'fatal_cleanup', 'active_stop': 'cancel_cleanup'}[phase] + '_clock'
        clocks = [s for s in steps if event(s, clock, 'java')]
        start = next((s['frame']['body'] for s in steps if message(s, 'start')), {})
        stops = [s for s in steps if message(s, 'stop', 'java')]
        budget = start.get('startup_ms') if phase == 'startup' else stops[-1]['frame']['body']['budget_ms'] if phase == 'stop' and stops else start.get('cancel_ms')
        if not ('java' in targets and len(clocks) == 2 and budget):
            return False
        valid = clocks[0]['args']['elapsed_ms'] == budget - 1 and clocks[1]['args']['elapsed_ms'] == budget and 'no_child_exit' in clocks[0]['expect'] and 'force_nonzero_exit' in clocks[1]['expect'] and 'no_stopped' in clocks[1]['expect']
        valid &= any(event(s, 'hold_cleanup', 'java') for s in steps) and not any(event(s, 'completion') or event(s, 'application_cleanup_complete') or event(s, 'stop_half_deadline') for s in own)
        valid &= event(steps[-1], 'child_exit', 'go') and steps[-1]['args']['exit_code'] != 0 and 'reset_rejected' in steps[-1]['expect']
        if phase == 'active_stop':
            cancels = [s for s in steps if message(s, 'cancel', 'java')]
            valid &= bool(cancels and stops) and cancels[-1]['frame']['body']['reason'] == 'stop' and 'arm_cleanup_watchdog' in cancels[-1]['expect'] and 'retain_earlier_cleanup_deadline' in stops[-1]['expect'] and budget < stops[-1]['frame']['body']['budget_ms']
        return valid
    if rule in ('cancel_wins_enqueue', 'enqueue_wins_cancel'):
        hold = indices(steps, lambda s: event(s, 'hold_release_enqueue'))
        ready = indices(steps, lambda s: event(s, 'runtime_arrive_returns') and 'enqueue_held' in s['expect'])
        cancel = indices(steps, lambda s: event(s, 'cancel_context'))
        resume = indices(steps, lambda s: event(s, 'resume_release_enqueue'))
        if not (hold and ready and cancel and resume and targets == ['go']):
            return False
        valid = hold[0] < ready[0] < min(cancel[0], resume[0]) and 'cancel_latched_atomically' in steps[cancel[0]]['expect']
        identity = steps[hold[0]]['args']['identity']
        valid &= steps[ready[0]]['args'] == {'identity': identity, 'result': 'nil'} and steps[resume[0]]['args']['identity'] == identity and steps[cancel[0]]['args']['invocation'] == identity['invocation']
        outputs = [s for s in own if s.get('delivery') == 'exchange' and s['peer'] == 'java']
        expected_types = ['cancel'] if rule == 'cancel_wins_enqueue' else ['release', 'cancel']
        valid &= [s['frame']['type'] for s in outputs] == expected_types
        valid &= all(s['frame']['body']['invocation'] == identity['invocation'] and s['frame']['body']['worker'] == identity['worker'] for s in outputs)
        if rule == 'cancel_wins_enqueue':
            return valid and cancel[0] < resume[0] and 'no_release' in steps[resume[0]]['expect'] and not any(message(s, 'release') for s in own)
        return valid and resume[0] < cancel[0] and 'release_enqueued_atomically' in steps[resume[0]]['expect'] and outputs[0]['frame']['body'] == identity
    if rule == 'readiness_rejection':
        return targets == ['go'] and any(message(s, 'ready', 'go') and s['delivery'] == 'input' and 'startup_error' in s['expect'] for s in own)
    if rule == 'canceled_reuse':
        retire = indices(own, lambda s: message(s, 'terminal', 'go') and s['frame']['body']['error'] and s['frame']['body']['error']['kind'] == 'cancelled' and 'retire_invocation' in s['expect'])
        calls = indices(own, lambda s: event(s, 'invoke_call', 'go'))
        return bool(retire and calls) and retire[0] < calls[-1] and own[calls[-1]]['args']['context'] == 'fresh'
    if rule == 'normal_active_stop':
        terminal = indices(own, lambda s: message(s, 'terminal', 'go'))
        stopped = indices(own, lambda s: message(s, 'stopped', 'go'))
        unwind = indices(own, lambda s: event(s, 'runtime_arrive_returns') and s['args']['result'] == 'cancelled')
        return case['prefix'] == 'arrived' and bool(terminal and stopped and unwind) and unwind[0] < terminal[0] < stopped[0] and 'stop_still_pending' in own[stopped[0]]['expect']
    if rule == 'exception_input':
        return 'java' in targets and any(event(s, 'command_exception', 'java') for s in own) and any(message(s, 'terminal', 'go') and s['frame']['body']['error'] for s in own)
    return False


def framed(raw):
    need(len(raw) >= 4 and int.from_bytes(raw[:4], 'big') == len(raw) - 4, 'framing length mismatch')
    return raw[4:]


def validate(data):
    need(data.get('vector_format') == 1 and data.get('wire_version') == 1, 'vector version')
    all_ids = [c['id'] for c in data['cases'] + data['framing']]
    need(len(all_ids) == len(set(all_ids)), 'duplicate case ID')
    cases = {c['id']: c for c in data['cases']}
    expanded = {}
    for case in data['cases']:
        try:
            need(set(case) == {'id', 'prefix', 'steps', 'covers', 'targets', 'execution'}, 'case fields')
            expanded[case['id']] = expand(data, case['prefix']) + case['steps']
            history(case, expanded[case['id']])
        except (ValueError, KeyError, TypeError) as err:
            raise ValueError(case['id'] + ': ' + str(err)) from err
    need(set(data['coverage']) == REQUIRED, 'coverage matrix families')
    for rule, names in data['coverage'].items():
        need(names and len(names) == len(set(names)), rule + ': empty/duplicate coverage')
        for name in names:
            need(name in cases and coverage(rule, cases[name], expanded[name]), rule + ': missing premise in ' + name)
    for c in data['framing']:
        need(c['targets'] == ['go', 'java'], 'framing decoder scope')
        raw = bytes.fromhex(c['input_hex'])
        if 'decoded' in c:
            actual = decode(framed(raw))
            frame_shape(actual)
            need(actual == c['decoded'], 'decoded framing control differs')
        if 'control_hex' in c:
            control = decode(framed(bytes.fromhex(c['control_hex'])))
            frame_shape(control)
            try:
                decode(framed(raw))
            except (ValueError, UnicodeError):
                pass
            else:
                raise ValueError(c['id'] + ': negative encoding input accepted strictly')
            permissive = json.loads(framed(raw).decode('utf-8', errors='replace'))
            frame_shape(permissive)


def self_test(data):
    def reject(label, mutate):
        changed = copy.deepcopy(data)
        mutate(changed)
        try:
            validate(changed)
        except (ValueError, KeyError, TypeError):
            print(label)
            return
        raise ValueError('self-test failed to reject ' + label)

    def case(d, name):
        return next(c for c in d['cases'] if c['id'] == name)

    reject('VECTOR_SCOPE_CAUGHT', lambda d: case(d, 'readiness_mismatch').update(targets=['java']))
    reject('VECTOR_DELIVERY_CAUGHT', lambda d: next(s for s in case(d, 'readiness_mismatch')['steps'] if message(s, 'ready')).update(delivery='exchange'))
    reject('VECTOR_INPUT_TARGET_CAUGHT', lambda d: case(d, 'future_release').update(targets=['go']))
    reject('VECTOR_MISSING_INVOKE_CAUGHT', lambda d: d['prefixes']['active'].__setitem__(slice(None), [s for s in d['prefixes']['active'] if not event(s, 'invoke_call')]))
    reject('VECTOR_SEQUENCE_CAUGHT', lambda d: next(s for s in d['prefixes']['active'] if message(s, 'accepted'))['frame'].update(seq=99))
    reject('VECTOR_DEADLINE_ORDER_CAUGHT', lambda d: next(s for s in case(d, 'stop_active_invocation')['steps'] if event(s, 'stop_call'))['expect'].reverse())
    reject('VECTOR_DUPLICATE_EFFECT_CAUGHT', lambda d: case(d, 'duplicate_invoke_java')['steps'][0]['expect'].remove('no_redispatch'))
    reject('VECTOR_LATE_FAULT_CAUGHT', lambda d: case(d, 'fatal_after_terminals')['steps'].__setitem__(slice(None), [s for s in case(d, 'fatal_after_terminals')['steps'] if not event(s, 'provisional_evaluation')]))
    reject('VECTOR_WATCHDOG_CAUGHT', lambda d: next(s for s in case(d, 'active_stop_cancel_watchdog_expires')['steps'] if event(s, 'advance_cancel_cleanup_clock') and s['args']['elapsed_ms'] == 1000)['args'].update(elapsed_ms=2500))
    reject('VECTOR_CANCEL_ARM_ORDER_CAUGHT', lambda d: next(s for s in case(d, 'stop_active_invocation')['steps'] if message(s, 'cancel'))['expect'].reverse())
    reject('VECTOR_ATOMIC_ORDER_CAUGHT', lambda d: next(s for s in case(d, 'cancel_wins_release_enqueue')['steps'] if event(s, 'resume_release_enqueue'))['expect'].__setitem__(0, 'send_release'))
    reject('VECTOR_EXCEPTION_SOURCE_CAUGHT', lambda d: case(d, 'rollback')['steps'].__setitem__(slice(None), [s for s in case(d, 'rollback')['steps'] if not event(s, 'command_exception')]))
    reject('VECTOR_FRAMING_CONTROL_CAUGHT', lambda d: next(c for c in d['framing'] if c['id'] == 'duplicate_json_key').update(input_hex='0000000d7b2276223a312c2276223a317d'))
    reject('VECTOR_COVERAGE_CAUGHT', lambda d: d['coverage'].pop('duplicate_java'))
    reject('VECTOR_PREFIX_CYCLE_CAUGHT', lambda d: d['prefixes']['new'].append({'prefix': 'new'}))
    print('EXTERNAL_SUT_VECTOR_SELF_TEST_RESULT mutations=rejected')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path', type=Path, nargs='?', default=DEFAULT)
    parser.add_argument('--self-test', action='store_true')
    args = parser.parse_args()
    try:
        data = decode(args.path.read_bytes())
        validate(data)
        if args.self_test:
            self_test(data)
        else:
            print('EXTERNAL_SUT_VECTOR_RESULT structure=valid scope=explicit coverage=present runtime=not_executed')
    except (ValueError, KeyError, TypeError, OSError) as err:
        print('external SUT vectors: ' + str(err), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
