#!/usr/bin/env python3
"""Test evidence accounting rejection paths using explicitly constructed logs."""
import json
from pathlib import Path
import runpy
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
RECORDER = runpy.run_path(str(ROOT / 'scripts/record-external-sut-go-results.py'))
COMMAND = 'go test ./internal/sut/external -v -count=20'


class RecordingTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.log = Path(self.directory.name) / 'go.log'
        self.entry = {'row': 'framing/zero_length', 'check': 'input/input_hex',
                      'handler': 'internal/sut/external/codec_test.go:TestSharedFraming'}

    def write(self, entries, suffix='', width=1):
        lines = []
        for offset in range(0, len(entries), width):
            lines.append('=== RUN   TestSharedFraming\n')
            lines.extend('EXTERNAL_SUT_CHECK ' + json.dumps(entry) + '\n'
                         for entry in entries[offset:offset + width])
            lines.append('--- PASS: TestSharedFraming (0.00s)\n')
        self.log.write_text(''.join(lines) + suffix +
                            'ok  github.com/weavegate/weavegate/internal/sut/external  0.1s\n')

    def record(self):
        return RECORDER['record'](self.log, 'a' * 40, COMMAND, 20, 'go1.25.0')

    def test_missing_checks_stay_incomplete(self):
        self.write([self.entry] * 20)
        result = self.record()
        self.assertTrue(all(r['status'] == 'incomplete' for r in result['results'].values()))
        self.assertEqual(len(result['results']['framing/zero_length']['checks']), 1)

    def test_complete_row_requires_every_check(self):
        assertion = dict(self.entry, check='expect/0/fatal_protocol')
        self.write([self.entry, assertion] * 20, width=2)
        result = self.record()
        self.assertEqual(result['results']['framing/zero_length']['status'], 'pass')
        self.assertEqual(result['results']['case/success']['status'], 'incomplete')

    def test_unknown_fields_checks_and_handlers(self):
        entries = [dict(self.entry, invented=True), dict(self.entry, check='invented'),
                   dict(self.entry, row='case/invented'), dict(self.entry, handler='missing.py')]
        for entry in entries:
            with self.subTest(entry=entry):
                self.write([entry] * 20)
                with self.assertRaises(ValueError):
                    self.record()

    def test_missing_handler_symbols(self):
        for symbol in ('NoSuchObserver', 'wrongReceiver.Read', '', 'TestSharedFraming:extra'):
            with self.subTest(symbol=symbol):
                self.write([dict(self.entry, handler='internal/sut/external/codec_test.go:' + symbol)] * 20)
                with self.assertRaises(ValueError):
                    self.record()

    def test_symbol_declarations_ignore_comments_and_literals(self):
        source = Path(self.directory.name) / 'observer.go'
        source.write_text('''package observer
// func LineComment() {}
/* func BlockComment() {} */
var example = `func RawString() {}`
var quoted = "func QuotedString() {}"
func RealObserver() {}
func (c *chunkReader) Read() {}
func (valueReader) Read() {}
''')
        self.assertEqual(RECORDER['source_symbols'](source),
                         {'RealObserver', 'chunkReader.Read', 'valueReader.Read'})

    def test_valid_method_reference(self):
        self.write([dict(self.entry, handler='internal/sut/external/codec_test.go:chunkReader.Read')] * 20)
        self.record()

    def test_repetition_boundaries_required(self):
        self.write([self.entry] * 20)
        valid = self.log.read_text()
        for malformed in (valid.replace('=== RUN   TestSharedFraming\n', ''),
                          valid.replace('--- PASS: TestSharedFraming (0.00s)\n', ''),
                          valid.replace('--- PASS:', '--- SKIP:'),
                          valid.replace('=== RUN', '=== PAUSE', 1)):
            with self.subTest(log=malformed[:100]):
                self.log.write_text(malformed)
                with self.assertRaises(ValueError):
                    self.record()

    def test_balanced_missing_and_duplicate_repetitions(self):
        lines = []
        for repetition in range(20):
            lines.append('=== RUN   TestSharedFraming\n')
            copies = 0 if repetition == 0 else 2 if repetition == 1 else 1
            lines.extend('EXTERNAL_SUT_CHECK ' + json.dumps(self.entry) + '\n' for _ in range(copies))
            lines.append('--- PASS: TestSharedFraming (0.00s)\n')
        self.log.write_text(''.join(lines) + 'ok  github.com/weavegate/weavegate/internal/sut/external  0.1s\n')
        with self.assertRaises(ValueError):
            self.record()

    def test_missing_and_duplicate_repetitions(self):
        for count in (0, 1, 19, 21):
            with self.subTest(count=count):
                self.write([self.entry] * count)
                with self.assertRaises(ValueError):
                    self.record()

    def test_failed_or_truncated_log(self):
        for suffix in ('FAIL\n', 'WARNING: DATA RACE\n', '--- FAIL: TestObserver\n'):
            self.write([self.entry] * 20, suffix)
            with self.assertRaises(ValueError):
                self.record()
        self.log.write_text('EXTERNAL_SUT_CHECK ' + json.dumps(self.entry))
        with self.assertRaises(ValueError):
            self.record()


if __name__ == '__main__':
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(RecordingTests)
    result = unittest.TextTestRunner().run(suite)
    if result.wasSuccessful():
        print('EXTERNAL_SUT_GO_RECORD_TEST_RESULT missing=incomplete unknown=rejected repetitions=checked failures=rejected')
    raise SystemExit(not result.wasSuccessful())
