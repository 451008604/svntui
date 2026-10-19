#!/usr/bin/env python3
"""Exercise fixture tool selection and POSIX/Windows verification paths without SVN IO."""
from pathlib import PurePosixPath, PureWindowsPath
import tempfile
import unittest
from unittest.mock import call, patch
import xml.etree.ElementTree as ET

import fixture


class MemoryIO:
    def exists(self):
        return False

    def read_bytes(self):
        return b'\xff'

    def write_bytes(self, data):
        return len(data)

    def write_text(self, data, **kwargs):
        return len(data)


class WindowsPath(MemoryIO, PureWindowsPath):
    pass


class PosixPath(MemoryIO, PurePosixPath):
    pass


class HistorySeedToolTest(unittest.TestCase):
    def test_prepare_selects_platform_safe_history_seed_tool(self):
        for platform, available_mucc in (('nt', '/tools/svnmucc'),
                                         ('posix', '/tools/svnmucc'),
                                         ('posix', None)):
            with self.subTest(platform=platform, available_mucc=available_mucc):
                with tempfile.TemporaryDirectory() as root:
                    builder = fixture.Fixture(root)
                    with patch.object(fixture.os, 'name', platform), patch.object(
                            fixture.shutil, 'which', side_effect=lambda name: (
                                available_mucc if name == 'svnmucc' else '/tools/' + name)) as which, patch.object(
                            builder, 'run') as run, patch.object(builder, 'svn'), patch.object(
                            builder, 'commit') as commit, patch.object(builder, 'write'):
                        builder.prepare(revisions=2, large_bytes=0, mode='basic')
                    history_message = 'history 002 ordinary'
                    if platform == 'posix' and available_mucc:
                        self.assertEqual(run.call_args.args[0][0], available_mucc)
                        self.assertIn(history_message, run.call_args.args[0])
                        self.assertEqual(commit.call_count, 1)  # Baseline only.
                    else:
                        commit.assert_any_call(history_message, 'history-0')
                        self.assertEqual(run.call_count, 1)  # svnadmin only.
                    if platform == 'nt':
                        self.assertNotIn(call('svnmucc'), which.call_args_list)
                    else:
                        self.assertIn(call('svnmucc'), which.call_args_list)


class VerifyPathsTest(unittest.TestCase):
    def test_nested_status_and_changelists_use_forward_slashes(self):
        for path_type, location in ((WindowsPath, r'C:\fixture\wc'),
                                    (PosixPath, '/fixture/wc')):
            with self.subTest(path_type=path_type.__name__):
                wc = path_type(location)
                status = ET.Element('status')
                target = ET.SubElement(status, 'target', path=str(wc))
                names = ['中文 文件.txt', 'space name.txt', 'foo', 'foo@',
                         'asset@2x.png', 'binary.bin', 'selected.txt']
                for name in names + ['untracked.txt', 'nested/added child.txt']:
                    entry = ET.SubElement(target, 'entry', path=str(wc / name))
                    item = 'unversioned' if name == 'untracked.txt' else (
                        'added' if name.startswith('nested/') else 'modified')
                    ET.SubElement(entry, 'wc-status', item=item, props='none')
                for group, paths in (('验收 列表', ['selected.txt', 'space name.txt']),
                                     ('nested group', ['nested/added child.txt'])):
                    changelist = ET.SubElement(status, 'changelist', name=group)
                    for name in paths:
                        entry = ET.SubElement(changelist, 'entry', path=str(wc / name))
                        ET.SubElement(entry, 'wc-status', item=(
                            'added' if name.startswith('nested/') else 'modified'), props='none')
                manifest = {'wc': str(wc), 'svn_config': 'config', 'mode': 'basic',
                            'large_bytes': 0, 'changelist': '验收 列表', 'repo_url': 'file:///repo',
                            'conflicts': {}, 'symlink': {}, 'evidence': str(wc.parent / 'evidence')}
                logs = b'<log><logentry revision="2"/><logentry revision="1"/></log>'
                with patch.object(fixture, 'Path', path_type), patch.object(
                        fixture.subprocess, 'check_output', side_effect=[ET.tostring(status), logs]):
                    report = fixture.verify(manifest)
                self.assertEqual(report['status_entries']['nested/added child.txt']['item'], 'added')
                self.assertEqual(report['changelists']['nested group'], ['nested/added child.txt'])
                self.assertTrue(all('\\' not in name for name in report['status_entries']))
                self.assertEqual(report['revision_count'], 2)
                self.assertFalse(report['browser_acceptance_executed'])


if __name__ == '__main__':
    unittest.main()
