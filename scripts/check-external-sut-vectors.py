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
normal_active_stop exception_input parent_startup_cleanup callback_terminal
unknown_outcome_fatal version_rejection incremented_arrival'''.split())
FATAL_KINDS = {'version', 'protocol', 'startup', 'transport', 'transaction', 'cleanup', 'shutdown'}
FRAMING = {
    'fragmented_valid_frame': ({'id', 'input_hex', 'read_chunk_sizes', 'expect', 'decoded', 'targets'}, ('one_ready_frame_after_complete_payload',)),
    'zero_length': ({'id', 'input_hex', 'expect', 'targets'}, ('fatal_protocol',)),
    'oversized_length': ({'id', 'input_hex', 'expect', 'targets'}, ('fatal_protocol', 'no_payload_allocation')),
    'partial_header_eof': ({'id', 'input_hex', 'eof', 'expect', 'targets'}, ('fatal_transport',)),
    'partial_payload_eof': ({'id', 'input_hex', 'eof', 'expect', 'targets'}, ('fatal_transport',)),
    'invalid_utf8': ({'id', 'input_hex', 'expect', 'control_hex', 'targets'}, ('fatal_protocol', 'no_dispatch')),
    'stdout_banner': ({'id', 'input_hex', 'expect', 'targets'}, ('fatal_protocol',)),
    'duplicate_json_key': ({'id', 'input_hex', 'expect', 'control_hex', 'targets'}, ('fatal_protocol', 'no_dispatch')),
    'trailing_value': ({'id', 'input_hex', 'expect', 'targets'}, ('fatal_protocol',)),
    'unknown_field': ({'id', 'input_hex', 'expect', 'targets'}, ('fatal_protocol',)),
    'duplicate_nested_json_key': ({'id', 'input_hex', 'control_hex', 'expect', 'targets'}, ('fatal_protocol', 'no_dispatch')),
}


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


def string(value, label, *, nonempty=False, max_bytes=None, name=False):
    need(isinstance(value, str), label + ' string')
    try:
        encoded = value.encode('utf-8')
    except UnicodeEncodeError as err:
        raise ValueError(label + ' Unicode scalar') from err
    need(not nonempty or bool(value), label + ' empty')
    need(max_bytes is None or len(encoded) <= max_bytes, label + ' too long')
    if name:
        need(bool(value) and len(encoded) <= 128 and value == value.strip(), label + ' name bounds')
        need(not any(ord(char) < 32 or 127 <= ord(char) <= 159 for char in value), label + ' control character')


def frame_shape(f):
    need(isinstance(f, dict) and set(f) == {'v', 'type', 'run', 'session', 'seq', 'body'}, 'frame envelope')
    need(integer(f['v'], 1, 2147483647) and integer(f['seq'], 1, 100000), 'version/sequence type or range')
    for key in ('run', 'session'):
        need(isinstance(f[key], str) and re.fullmatch('[a-f0-9]{32}', f[key]), key + ' identity')
    t, b = f['type'], f['body']
    need(t in BODY and isinstance(b, dict) and set(b) == BODY[t], 'message body fields')
    need(len(json.dumps(f, separators=(',', ':'), ensure_ascii=False).encode('utf-8')) <= 1048576, 'frame too large')
    if 'invocation' in b:
        need(isinstance(b['invocation'], str) and re.fullmatch('[a-f0-9]{32}', b['invocation']), 'invocation identity')
        string(b['worker'], 'worker', name=True)
    if 'arrival' in b:
        need(isinstance(b['arrival'], str) and re.fullmatch('[1-9][0-9]*', b['arrival']) and int(b['arrival']) <= 100000, 'arrival identity')
    for key in ('startup_ms', 'cancel_ms', 'budget_ms'):
        if key in b:
            need(integer(b[key], 1, 2147483647), 'invalid ' + key)
    if t in ('start', 'ready'):
        need(integer(b['capacity'], 1, 1024), 'capacity')
        for key in ('commands', 'points'):
            a = b[key]
            need(isinstance(a, list) and len(a) == len(set(a)), key)
            for value in a:
                string(value, key, name=True)
    if t == 'start':
        string(b['variant'], 'variant', name=True)
        db = b['database']
        need(isinstance(db, dict) and set(db) == {'driver', 'host', 'port', 'name', 'username', 'password'}, 'database fields')
        need(db['driver'] == 'mysql' and integer(db['port'], 1, 65535), 'database driver/port')
        for key in ('host', 'name', 'username'):
            string(db[key], 'database.' + key, nonempty=True)
        string(db['password'], 'database.password')
        need(isinstance(b['params'], dict), 'params')
        for key, value in b['params'].items():
            string(key, 'parameter key', name=True)
            string(value, 'parameter value')
    if t == 'invoke':
        string(b['command'], 'command', name=True)
    if t in ('arrive', 'release'):
        string(b['point'], 'point', name=True)
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
            need(err['kind'] in ('application', 'mysql', 'cancelled'), 'error kind')
            string(err['message'], 'error message', max_bytes=1024)
            need(integer(err['mysql_code'], 0, 65535), 'MySQL code')
            need((err['mysql_code'] > 0 and isinstance(err['sql_state'], str) and re.fullmatch('[A-Z0-9]{5}', err['sql_state'])) if err['kind'] == 'mysql' else (err['mysql_code'] == 0 and err['sql_state'] == ''), 'SQL error metadata')
    if t == 'fatal':
        need(b['kind'] in FATAL_KINDS, 'fatal kind')
        string(b['message'], 'fatal message', max_bytes=1024)


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


def injected_premise(case_id, frame, effects, invocations, outstanding, completed, canceled, start, stop_seen):
    body, kind = frame['body'], frame['type']
    invocation = body.get('invocation')
    current = outstanding.get(invocation)
    if case_id == 'cancel_racing_release':
        return kind == 'release' and invocation in canceled and current == body
    if case_id == 'late_arrival_after_cancel':
        return kind == 'arrive' and invocation in canceled and invocations.get(invocation, (None,))[0] == body['worker']
    if case_id == 'stale_session':
        return kind == 'arrive' and invocation in invocations and current is None and 'client_arrive_once' in effects
    if case_id == 'retired_invocation_worker_reuse':
        return kind in ('arrive', 'release') and invocation in completed and invocations.get(invocation, (None,))[0] == body['worker']
    if case_id == 'unknown_invocation':
        return invocation not in invocations
    if case_id == 'future_release':
        return kind == 'release' and current is not None and all(body[k] == current[k] for k in ('invocation', 'worker', 'point')) and int(body['arrival']) > int(current['arrival'])
    if case_id == 'wrong_point_release':
        return kind == 'release' and current is not None and all(body[k] == current[k] for k in ('invocation', 'worker', 'arrival')) and body['point'] != current['point']
    if case_id == 'release_before_arrival':
        return kind == 'release' and invocations.get(invocation, (None,))[0] == body['worker'] and current is None
    if case_id == 'terminal_while_arrived':
        return kind == 'terminal' and current is not None and invocations.get(invocation, (None,))[0] == body['worker']
    if case_id == 'readiness_mismatch':
        return kind == 'ready' and start is not None and any(body[k] != start[k] for k in ('commands', 'points', 'capacity'))
    if case_id == 'unsolicited_startup_stopped':
        return kind == 'stopped' and not stop_seen
    if case_id == 'fatal_after_terminals':
        return kind == 'fatal' and bool(invocations) and set(invocations) <= completed
    return False


def emissions(case, steps):
    message_effects = {
        'send_ready': 'ready', 'send_invoke': 'invoke', 'send_arrive': 'arrive',
        'send_release': 'release', 'send_cancel': 'cancel', 'send_stop': 'stop',
        'send_stopped': 'stopped', 'send_terminal': 'terminal', 'send_fatal': 'fatal',
    }
    fatal_effects = {'fatal_' + kind: kind for kind in FATAL_KINDS}
    used = set()
    for index, step in enumerate(steps):
        if step['peer'] not in case['targets']:
            continue
        for effect in step['expect']:
            kind = message_effects.get(effect)
            fatal_kind = fatal_effects.get(effect)
            if kind is None and fatal_kind is None:
                continue
            expected = kind or 'fatal'
            outputs = [(later_index, later) for later_index, later in enumerate(steps[index + 1:], index + 1)
                       if later_index not in used
                       and later.get('delivery') == 'exchange'
                       and later['peer'] != step['peer']
                       and message(later, expected)]
            need(outputs, case['id'] + ': ' + effect + ' lacks explicit wire exchange')
            output_index, output = outputs[0]
            used.add(output_index)
            if fatal_kind:
                need(output['frame']['body']['kind'] == fatal_kind,
                     case['id'] + ': ' + effect + ' emits wrong fatal kind')


def history(case, steps):
    targets = case['targets']
    need(case['execution'] == 'isolated' and targets and len(targets) == len(set(targets)) and set(targets) <= {'go', 'java'}, 'case scope')
    seq = {'go': {}, 'java': {}}
    invocations, active, sources, reasons = {}, {}, {}, {}
    outstanding, completed, canceled = {}, set(), set()
    pending_arrivals, last_arrival = {}, {}
    binding = None
    start = None
    stop_seen = False
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
            if event(step, 'worker_arrives', 'java'):
                identity = a['identity']
                iid = identity['invocation']
                need(invocations.get(iid, (None,))[0] == identity['worker'], 'arrival event changes invocation binding')
                need(iid not in outstanding and iid not in pending_arrivals, 'arrival event while gate is live')
                need(int(identity['arrival']) == last_arrival.get(iid, 0) + 1, 'arrival event does not increment')
                pending_arrivals[iid] = identity
            if event(step, 'command_exception', 'java'):
                sources[a['invocation']] = a['exception']
            if event(step, 'cancel_context', 'go'):
                need(a['invocation'] in invocations, 'cancel lacks invocation')
                canceled.add(a['invocation'])
            if event(step, 'runtime_arrive_returns', 'go'):
                identity = a['identity']
                need(outstanding.get(identity['invocation']) == identity,
                     'runtime return changes arrival binding')
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
            need(injected_premise(case['id'], f, effects, invocations, outstanding, completed, canceled, start, stop_seen), 'injected input lacks declared lifecycle premise')
            continue
        if t == 'invoke':
            need(invocations.get(b['invocation']) == (b['worker'], b['command']), 'invoke lacks Go Handle call')
        if t == 'accepted':
            need(invocations.get(b['invocation'], (None,))[0] == b['worker'], 'accepted changes invocation binding')
        if t == 'arrive':
            need(invocations.get(b['invocation'], (None,))[0] == b['worker'], 'arrival changes invocation binding')
            need(pending_arrivals.get(b['invocation']) == b and b['invocation'] not in outstanding, 'arrival lacks matching gate event')
            outstanding[b['invocation']] = b
            last_arrival[b['invocation']] = int(b['arrival'])
            pending_arrivals.pop(b['invocation'])
        if t == 'release':
            need(outstanding.get(b['invocation']) == b, 'release lacks matching arrival')
            outstanding.pop(b['invocation'])
        if t == 'cancel':
            need(invocations.get(b['invocation'], (None,))[0] == b['worker'], 'cancel changes invocation binding')
            reasons.setdefault(b['invocation'], b['reason'])
            canceled.add(b['invocation'])
            if 'request_jdbc_cancel' in effects and 'java' in targets:
                # An already-canceled invocation does not need another watchdog.
                need('cancel_latched' not in effects or ('arm_cleanup_watchdog' in effects and effects.index('arm_cleanup_watchdog') < effects.index('request_jdbc_cancel')), 'Java cancellation watchdog must precede JDBC cancel')
        if t == 'terminal':
            iid, err = b['invocation'], b['error']
            need(invocations.get(iid, (None,))[0] == b['worker'], 'terminal changes invocation binding')
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
                completed.add(iid)
        if t == 'stop':
            stop_seen = True


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
        clock_positions = indices(steps, lambda s: event(s, clock, 'java'))
        clocks = [steps[index] for index in clock_positions]
        start = next((s['frame']['body'] for s in steps if message(s, 'start')), {})
        stops = [s for s in steps if message(s, 'stop', 'java')]
        budget = start.get('startup_ms') if phase == 'startup' else stops[-1]['frame']['body']['budget_ms'] if phase == 'stop' and stops else start.get('cancel_ms')
        trigger_effect = {'startup': 'arm_startup_watchdog', 'stop': 'arm_stop_watchdog',
                          'cancel': 'arm_cleanup_watchdog', 'fatal': 'arm_cleanup_watchdog',
                          'active_stop': 'arm_cleanup_watchdog'}[phase]
        trigger_type = {'startup': 'start', 'stop': 'stop', 'cancel': 'cancel',
                        'fatal': 'fatal', 'active_stop': 'cancel'}[phase]
        triggers = indices(steps, lambda s: message(s, trigger_type, 'java') and trigger_effect in s['expect'])
        exits = indices(steps, lambda s: event(s, 'child_exit', 'go'))
        if not ('java' in targets and len(clocks) == 2 and budget and triggers and exits):
            return False
        valid = clocks[0]['args']['elapsed_ms'] == budget - 1 and clocks[1]['args']['elapsed_ms'] == budget and 'no_child_exit' in clocks[0]['expect'] and 'force_nonzero_exit' in clocks[1]['expect'] and 'no_stopped' in clocks[1]['expect']
        valid &= triggers[-1] < clock_positions[0] < clock_positions[1] < exits[-1]
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
    if rule == 'parent_startup_cleanup':
        deadline = indices(own, lambda s: event(s, 'startup_deadline', 'go'))
        cutoff = indices(own, lambda s: event(s, 'stop_half_deadline', 'go'))
        exited = indices(own, lambda s: event(s, 'child_exit', 'go'))
        return targets == ['go'] and bool(deadline and cutoff and exited) and deadline[0] < cutoff[0] < exited[0] and 'kill_child' in own[cutoff[0]]['expect'] and {'child_reaped', 'startup_error', 'reset_rejected'} <= set(own[exited[0]]['expect'])
    if rule == 'callback_terminal':
        completions = [s for s in own if event(s, 'completion', 'java')]
        terminals = [s for s in own if message(s, 'terminal', 'go') and s.get('delivery') == 'exchange']
        if targets != ['go', 'java'] or len(completions) != 3 or len(terminals) != 1:
            return False
        body = terminals[0]['frame']['body']
        return completions[0]['args'] == {'proxy_exited': False, 'transaction': 'committed', 'lease': 'held'} and completions[1]['args'] == {'proxy_exited': True, 'transaction': 'committed', 'lease': 'held'} and completions[2]['args'] == {'proxy_exited': True, 'transaction': 'committed', 'lease': 'returned'} and body['transaction'] == 'committed' and body['connection'] == 'returned' and body['error'] is None
    if rule == 'unknown_outcome_fatal':
        completion = indices(own, lambda s: event(s, 'completion', 'java') and s['args'].get('transaction') == 'unknown')
        fatal = indices(own, lambda s: message(s, 'fatal', 'go') and s.get('delivery') == 'exchange' and s['frame']['body']['kind'] == 'transaction')
        return targets == ['go', 'java'] and bool(completion and fatal) and completion[0] < fatal[0] and {'latch_adapter_fault', 'no_worker_result', 'abort_run', 'quarantine_fixture'} <= set(own[fatal[0]]['expect'])
    if rule == 'version_rejection':
        bad = indices(own, lambda s: message(s, 'start', 'java') and s.get('delivery') == 'input' and s['frame']['v'] != 1 and 'fatal_version' in s['expect'])
        fatal = indices(own, lambda s: message(s, 'fatal', 'go') and s.get('delivery') == 'exchange' and s['frame']['body']['kind'] == 'version' and s['frame']['seq'] == 1)
        exited = indices(own, lambda s: event(s, 'child_exit', 'go') and s['args']['exit_code'] != 0)
        return targets == ['java'] and bool(bad and fatal and exited) and bad[0] < fatal[0] < exited[0] and {'no_ready', 'no_terminal', 'no_stopped'} <= set(own[exited[0]]['expect'])
    if rule == 'incremented_arrival':
        arrivals = [s for s in own if message(s, 'arrive', 'go') and s.get('delivery') == 'exchange']
        releases = [s for s in own if message(s, 'release', 'java') and s.get('delivery') == 'exchange']
        if targets != ['go', 'java'] or len(arrivals) != 1 or len(releases) != 1:
            return False
        arrival, release = arrivals[0]['frame']['body'], releases[0]['frame']['body']
        return case['prefix'] == 'released' and arrival == release and arrival['arrival'] == '2' and arrival['point'] == 'before_write'
    return False


def framed(raw):
    need(len(raw) >= 4 and int.from_bytes(raw[:4], 'big') == len(raw) - 4, 'framing length mismatch')
    return raw[4:]


def validate_framing(case):
    name = case['id']
    need(name in FRAMING, 'unknown framing case: ' + name)
    fields, effects = FRAMING[name]
    need(set(case) == fields and tuple(case['expect']) == effects, name + ': framing fields/effects')
    need(case['targets'] == ['go', 'java'], name + ': framing decoder scope')
    value = case['input_hex']
    need(isinstance(value, str) and len(value) % 2 == 0 and re.fullmatch('[0-9a-f]*', value), name + ': input hex')
    raw = bytes.fromhex(value)

    if name == 'fragmented_valid_frame':
        actual = decode(framed(raw))
        frame_shape(actual)
        need(actual == case['decoded'], name + ': decoded control differs')
        chunks = case['read_chunk_sizes']
        need(isinstance(chunks, list) and all(integer(v, 1, len(raw)) for v in chunks) and sum(chunks) < len(raw), name + ': chunk plan')
        return
    if name == 'zero_length':
        need(raw == b'\x00\x00\x00\x00', name + ': not a zero length header')
        return
    if name == 'oversized_length':
        need(len(raw) == 4 and int.from_bytes(raw, 'big') > 1048576, name + ': not an allocation-free oversized header')
        return
    if name == 'partial_header_eof':
        need(case['eof'] is True and 0 < len(raw) < 4, name + ': not a partial header EOF')
        return
    if name == 'partial_payload_eof':
        need(case['eof'] is True and len(raw) >= 4 and 1 <= int.from_bytes(raw[:4], 'big') <= 1048576 and len(raw) - 4 < int.from_bytes(raw[:4], 'big'), name + ': not a partial payload EOF')
        return
    if name == 'stdout_banner':
        need(raw == b'Spring Boot\n', name + ': banner premise')
        return
    if name == 'trailing_value':
        payload = framed(raw).decode('utf-8')
        first, end = json.JSONDecoder(object_pairs_hook=unique).raw_decode(payload)
        frame_shape(first)
        need(payload[end:] and payload[end:].strip() == '{}', name + ': missing trailing JSON value')
        return
    if name == 'unknown_field':
        invalid = decode(framed(raw))
        need(set(invalid) == {'v', 'type', 'run', 'session', 'seq', 'body', 'extra'} and invalid['extra'] is True, name + ': unknown-field premise')
        invalid.pop('extra')
        frame_shape(invalid)
        return

    control_value = case['control_hex']
    need(isinstance(control_value, str) and len(control_value) % 2 == 0 and re.fullmatch('[0-9a-f]+', control_value), name + ': control hex')
    control = decode(framed(bytes.fromhex(control_value)))
    frame_shape(control)
    try:
        decode(framed(raw))
    except UnicodeDecodeError:
        need(name == 'invalid_utf8', name + ': wrong rejection cause')
    except ValueError as err:
        need(name in ('duplicate_json_key', 'duplicate_nested_json_key') and str(err).startswith('duplicate JSON key:'), name + ': wrong rejection cause')
    else:
        raise ValueError(name + ': negative encoding input accepted strictly')
    if name == 'invalid_utf8':
        permissive = json.loads(framed(raw).decode('utf-8', errors='replace'))
        frame_shape(permissive)
    else:
        need(json.loads(framed(raw).decode('utf-8')) == control, name + ': duplicate-key control differs')


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
            emissions(case, expanded[case['id']])
        except (ValueError, KeyError, TypeError) as err:
            raise ValueError(case['id'] + ': ' + str(err)) from err
    need(set(data['coverage']) == REQUIRED, 'coverage matrix families')
    for rule, names in data['coverage'].items():
        need(names and len(names) == len(set(names)), rule + ': empty/duplicate coverage')
        for name in names:
            need(name in cases and coverage(rule, cases[name], expanded[name]), rule + ': missing premise in ' + name)
    need({c['id'] for c in data['framing']} == set(FRAMING), 'framing inventory')
    for c in data['framing']:
        validate_framing(c)


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

    def move_clocks_before_trigger(d):
        steps = case(d, 'java_fatal_cleanup_watchdog_expires')['steps']
        clocks = [step for step in steps if event(step, 'advance_fatal_cleanup_clock')]
        steps[:] = clocks + [step for step in steps if not event(step, 'advance_fatal_cleanup_clock')]

    def reset_second_arrival(d):
        for step in case(d, 'incremented_arrival')['steps']:
            identity = step.get('args', {}).get('identity') or step.get('frame', {}).get('body')
            if isinstance(identity, dict) and 'arrival' in identity:
                identity['arrival'] = '1'

    reject('VECTOR_SCOPE_CAUGHT', lambda d: case(d, 'readiness_mismatch').update(targets=['java']))
    reject('VECTOR_DELIVERY_CAUGHT', lambda d: next(s for s in case(d, 'readiness_mismatch')['steps'] if message(s, 'ready')).update(delivery='exchange'))
    reject('VECTOR_INPUT_TARGET_CAUGHT', lambda d: case(d, 'future_release').update(targets=['go']))
    reject('VECTOR_FUTURE_RELEASE_CAUGHT', lambda d: next(s for s in case(d, 'future_release')['steps'] if message(s, 'release'))['frame']['body'].update(arrival='1'))
    reject('VECTOR_WRONG_POINT_CAUGHT', lambda d: next(s for s in case(d, 'wrong_point_release')['steps'] if message(s, 'release'))['frame']['body'].update(point='after_read'))
    reject('VECTOR_RELEASE_BEFORE_ARRIVAL_CAUGHT', lambda d: case(d, 'release_before_arrival').update(prefix='arrived'))
    reject('VECTOR_TERMINAL_WHILE_ARRIVED_CAUGHT', lambda d: case(d, 'terminal_while_arrived').update(prefix='released'))
    reject('VECTOR_MISSING_INVOKE_CAUGHT', lambda d: d['prefixes']['active'].__setitem__(slice(None), [s for s in d['prefixes']['active'] if not event(s, 'invoke_call')]))
    reject('VECTOR_ACCEPTED_BINDING_CAUGHT', lambda d: next(s for s in d['prefixes']['active'] if message(s, 'accepted'))['frame']['body'].update(worker='w2'))
    reject('VECTOR_ARRIVE_BINDING_CAUGHT', lambda d: next(s for s in d['prefixes']['arrived'] if message(s, 'arrive'))['frame']['body'].update(worker='w2'))
    reject('VECTOR_CANCEL_BINDING_CAUGHT', lambda d: next(s for s in case(d, 'stop_active_invocation')['steps'] if message(s, 'cancel'))['frame']['body'].update(worker='w2'))
    reject('VECTOR_TERMINAL_BINDING_CAUGHT', lambda d: next(s for s in d['prefixes']['completed'] if message(s, 'terminal'))['frame']['body'].update(worker='w2'))
    reject('VECTOR_RUNTIME_BINDING_CAUGHT', lambda d: next(s for s in d['prefixes']['released'] if event(s, 'runtime_arrive_returns'))['args']['identity'].update(worker='w2'))
    reject('VECTOR_SEQUENCE_CAUGHT', lambda d: next(s for s in d['prefixes']['active'] if message(s, 'accepted'))['frame'].update(seq=99))
    reject('VECTOR_DEADLINE_ORDER_CAUGHT', lambda d: next(s for s in case(d, 'stop_active_invocation')['steps'] if event(s, 'stop_call'))['expect'].reverse())
    reject('VECTOR_DUPLICATE_EFFECT_CAUGHT', lambda d: case(d, 'duplicate_invoke_java')['steps'][0]['expect'].remove('no_redispatch'))
    reject('VECTOR_LATE_FAULT_CAUGHT', lambda d: case(d, 'fatal_after_terminals')['steps'].__setitem__(slice(None), [s for s in case(d, 'fatal_after_terminals')['steps'] if not event(s, 'provisional_evaluation')]))
    reject('VECTOR_WATCHDOG_CAUGHT', lambda d: next(s for s in case(d, 'active_stop_cancel_watchdog_expires')['steps'] if event(s, 'advance_cancel_cleanup_clock') and s['args']['elapsed_ms'] == 1000)['args'].update(elapsed_ms=2500))
    reject('VECTOR_WATCHDOG_TRIGGER_ORDER_CAUGHT', move_clocks_before_trigger)
    reject('VECTOR_CANCEL_ARM_ORDER_CAUGHT', lambda d: next(s for s in case(d, 'stop_active_invocation')['steps'] if message(s, 'cancel'))['expect'].reverse())
    reject('VECTOR_ATOMIC_ORDER_CAUGHT', lambda d: next(s for s in case(d, 'cancel_wins_release_enqueue')['steps'] if event(s, 'resume_release_enqueue'))['expect'].__setitem__(0, 'send_release'))
    reject('VECTOR_EXCEPTION_SOURCE_CAUGHT', lambda d: case(d, 'rollback')['steps'].__setitem__(slice(None), [s for s in case(d, 'rollback')['steps'] if not event(s, 'command_exception')]))
    reject('VECTOR_FATAL_KIND_CAUGHT', lambda d: next(s for s in case(d, 'unknown_commit_outcome')['steps'] if message(s, 'fatal'))['frame']['body'].update(kind='bogus'))
    reject('VECTOR_DATABASE_VALUE_CAUGHT', lambda d: next(s for s in d['prefixes']['ready'] if message(s, 'start'))['frame']['body']['database'].update(host=''))
    reject('VECTOR_NAME_BOUND_CAUGHT', lambda d: next(s for s in d['prefixes']['active'] if message(s, 'invoke'))['frame']['body'].update(command='x' * 129))
    reject('VECTOR_MESSAGE_BOUND_CAUGHT', lambda d: next(s for s in case(d, 'unknown_commit_outcome')['steps'] if message(s, 'fatal'))['frame']['body'].update(message='x' * 1025))
    reject('VECTOR_STARTUP_REAP_CAUGHT', lambda d: case(d, 'startup_deadline')['steps'].pop())
    reject('VECTOR_CALLBACK_TERMINAL_CAUGHT', lambda d: case(d, 'completion_callback_too_early')['steps'].pop())
    reject('VECTOR_UNKNOWN_FATAL_CAUGHT', lambda d: case(d, 'unknown_commit_outcome')['steps'].pop())
    reject('VECTOR_VERSION_FATAL_CAUGHT', lambda d: case(d, 'version_mismatch')['steps'].__setitem__(slice(None), [s for s in case(d, 'version_mismatch')['steps'] if not message(s, 'fatal')]))
    reject('VECTOR_OUTPUT_EXCHANGE_CAUGHT', lambda d: case(d, 'late_arrival_after_cancel')['steps'].__setitem__(slice(None), [s for s in case(d, 'late_arrival_after_cancel')['steps'] if not message(s, 'cancel')]))
    reject('VECTOR_ARRIVAL_INCREMENT_CAUGHT', reset_second_arrival)
    valid_hex = next(c for c in data['framing'] if c['id'] == 'fragmented_valid_frame')['input_hex']
    reject('VECTOR_FRAMING_INVENTORY_CAUGHT', lambda d: d.update(framing=[]))
    for framing_name in sorted(FRAMING):
        replacement = '00000000' if framing_name == 'fragmented_valid_frame' else valid_hex
        reject('VECTOR_FRAMING_' + framing_name.upper() + '_CAUGHT', lambda d, name=framing_name, value=replacement: next(c for c in d['framing'] if c['id'] == name).update(input_hex=value))
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
