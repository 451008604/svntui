#!/usr/bin/env python3
"""Exercise verification output with real POSIX/Windows path semantics, without SVN IO."""
from pathlib import PurePosixPath, PureWindowsPath
import unittest
from unittest.mock import patch
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
