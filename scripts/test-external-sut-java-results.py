#!/usr/bin/env python3
"""Test Java evidence accounting rejection paths using explicitly constructed logs."""
import json
from pathlib import Path
import runpy
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
RECORDER = runpy.run_path(str(ROOT / 'scripts/record-external-sut-java-results.py'))
COMMAND = './mvnw -B verify -Dweavegate.repetitions=20'
HANDLER = 'sdk/java/src/test/java/io/github/weavegate/sdk/FramingHarness.java:FramingHarness.run'
VERSIONS = {'java': '21', 'spring': 'Boot 4.0.8', 'transaction_manager': 'DataSourceTransactionManager',
            'jdbc_driver': 'Connector/J 9.7.0', 'pool': 'HikariCP 7.0.2', 'build_tool': 'Maven 3.9.16'}
TEST = '[engine:junit-jupiter][class:X][test-factory:framingCases()][dynamic-container:#2]'


class RecordingTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.log = Path(self.directory.name) / 'java.log'
        self.build = Path(self.directory.name) / 'build.log'
        self.build.write_text('[INFO] Tests run: 1, Failures: 0, Errors: 0, Skipped: 0\n[INFO] BUILD SUCCESS\n')
        self.entry = {'row': 'framing/zero_length', 'check': 'input/input_hex', 'handler': HANDLER}

    def write(self, per_repetition, repetitions=20, versions=VERSIONS, test=TEST, status='PASS'):
        lines = ['EXTERNAL_SUT_VERSIONS ' + json.dumps(versions)]
        for n in range(1, repetitions + 1):
            identity = json.dumps({'test': test, 'repetition': n})
            lines.append('EXTERNAL_SUT_TEST_RUN ' + identity)
            lines.extend('EXTERNAL_SUT_CHECK ' + json.dumps(e) for e in per_repetition)
            lines.append('EXTERNAL_SUT_TEST_' + status + ' ' + identity)
        self.log.write_text('\n'.join(lines) + '\n')

    def record(self, command=COMMAND):
        return RECORDER['record'](self.log, self.build, 'a' * 40, command, 20)

    def test_missing_checks_stay_incomplete(self):
        self.write([self.entry])
        result = self.record()
        self.assertTrue(all(r['status'] == 'incomplete' for r in result['results'].values()))
        self.assertEqual(result['run']['versions'], VERSIONS)

    def test_complete_row_requires_every_check(self):
        self.write([self.entry, dict(self.entry, check='expect/0/fatal_protocol')])
        result = self.record()
        self.assertEqual(result['results']['framing/zero_length']['status'], 'pass')
        self.assertEqual(result['results']['case/success']['status'], 'incomplete')

    def test_unknown_fields_checks_rows_and_handlers(self):
        for entry in (dict(self.entry, invented=True), dict(self.entry, check='invented'),
                      dict(self.entry, row='case/late_arrival_after_cancel'),
                      dict(self.entry, handler='internal/sut/external/codec_test.go:TestSharedFraming'),
                      dict(self.entry, handler=HANDLER.replace('run', 'noSuchObserver')),
                      dict(self.entry, handler=HANDLER.replace('FramingHarness.run', 'Other.run'))):
            with self.subTest(entry=entry):
                self.write([entry])
                with self.assertRaises(ValueError):
                    self.record()

    def test_symbols_ignore_comments_and_literals(self):
        source = Path(self.directory.name) / 'Observer.java'
        source.write_text('''class Observer {
    // void lineComment() {}
    /* void blockComment() {} */
    String quoted = "void quoted() {}";
    String block = """
        void textBlock() {}
        """;
    void real() {
        switch (x) {
        }
    }
    static <T> java.util.List<T> generic(T value) throws Exception {
        return null;
    }
}
''')
        self.assertEqual(RECORDER['source_symbols'](source), {'Observer.real', 'Observer.generic'})

    def test_symbols_belong_to_the_declaring_type(self):
        source = Path(self.directory.name) / 'Observers.java'
        source.write_text('''class First {
    void run() {}
    class Nested {
        void inspect() {}
    }
    Runnable callback = new Runnable() {
        public void anonymous() {}
    };
}
class Second {
    void observe() {}
}
''')
        self.assertEqual(RECORDER['source_symbols'](source),
                         {'First.run', 'First.Nested.inspect', 'Second.observe'})
        self.write([dict(self.entry, handler='sdk/java/src/test/java/io/github/weavegate/sdk/'
                                            'VectorHarness.java:Context.run')])
        with self.assertRaises(ValueError):
            self.record()

    def test_repetition_count_and_command(self):
        for count in (19, 21):
            with self.subTest(count=count):
                self.write([self.entry], repetitions=count)
                with self.assertRaises(ValueError):
                    self.record()
        self.write([self.entry])
        for command in ('./mvnw -B verify', './mvnw -B verify -Dweavegate.repetitions=2'):
            with self.subTest(command=command):
                with self.assertRaises(ValueError):
                    self.record(command)

    def test_duplicate_or_missing_check_within_repetition(self):
        self.write([self.entry, self.entry])
        with self.assertRaises(ValueError):
            self.record()
        self.write([self.entry])
        text = self.log.read_text().replace('EXTERNAL_SUT_CHECK ' + json.dumps(self.entry) + '\n', '', 1)
        self.log.write_text(text)
        with self.assertRaises(ValueError):
            self.record()

    def test_failures_boundaries_and_versions(self):
        for status in ('FAIL', 'SKIP'):
            with self.subTest(status=status):
                self.write([self.entry], status=status)
                with self.assertRaises(ValueError):
                    self.record()
        self.write([self.entry])
        valid = self.log.read_text()
        for malformed in (valid.replace('EXTERNAL_SUT_TEST_RUN', 'IGNORED', 1),
                          valid.replace('EXTERNAL_SUT_TEST_PASS', 'IGNORED', 1),
                          valid.replace('EXTERNAL_SUT_VERSIONS', 'IGNORED'),
                          valid.replace('"repetition": 2}', '"repetition": 1}')):
            with self.subTest(log=malformed[:60]):
                self.log.write_text(malformed)
                with self.assertRaises(ValueError):
                    self.record()
        self.write([self.entry], versions=dict(VERSIONS, pool='HikariCP null'))
        with self.assertRaises(ValueError):
            self.record()

    def test_failed_build_log(self):
        self.write([self.entry])
        for build in ('[INFO] BUILD FAILURE\n', '[ERROR] Tests run: 1, Failures: 1\n[INFO] BUILD SUCCESS\n'):
            with self.subTest(build=build):
                self.build.write_text(build)
                with self.assertRaises(ValueError):
                    self.record()


if __name__ == '__main__':
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(RecordingTests)
    result = unittest.TextTestRunner().run(suite)
    if result.wasSuccessful():
        print('EXTERNAL_SUT_JAVA_RECORD_TEST_RESULT missing=incomplete unknown=rejected repetitions=checked failures=rejected')
    raise SystemExit(not result.wasSuccessful())
