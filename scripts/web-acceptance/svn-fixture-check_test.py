#!/usr/bin/env python3
"""Run the production peer-change helper and verify its exact-byte SVN merge contract."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET


@unittest.skipUnless(shutil.which('svn') and shutil.which('svnadmin'), 'SVN unavailable')
class PeerNewlineContractTest(unittest.TestCase):
    def test_exact_utf8_peer_write_preserves_nonoverlapping_svn_merge(self):
        with tempfile.TemporaryDirectory(prefix='svntui-peer-newline-') as directory:
            root = Path(directory)
            repo, wc, peer = (root / name for name in ('repo', 'wc', 'peer'))
            config, evidence = root / 'config', root / 'evidence'
            config.mkdir()
            evidence.mkdir()

            def run(*args):
                return subprocess.check_output([str(arg) for arg in args], stderr=subprocess.STDOUT, timeout=60)

            def svn(*args):
                return run('svn', *args, '--non-interactive', '--config-dir', config)

            run('svnadmin', 'create', repo)
            svn('checkout', repo.as_uri(), wc)
            lines = [f'line {index:02d} baseline\n' for index in range(1, 21)]
            file = wc / 'update-target.txt'
            file.write_bytes(''.join(lines).encode('utf-8'))
            svn('add', str(file) + '@')
            svn('commit', '--message', 'baseline', str(wc) + '@')
            svn('checkout', repo.as_uri(), peer)
            local = lines.copy()
            local[0] = 'local line 01 中文\n'
            file.write_bytes(''.join(local).encode('utf-8'))
            remote = lines.copy()
            remote[-1] = 'peer line 20 中文\n'
            requested = ''.join(remote)
            manifest = root / 'fixture.json'
            manifest.write_bytes(json.dumps({'wc': str(wc), 'peer': str(peer),
                'repo_url': repo.as_uri(), 'svn_config': str(config), 'evidence': str(evidence)}).encode('utf-8'))
            script = Path(__file__).with_name('svn-fixture-check.py').resolve()
            # Reproduce Windows default text newline translation on every platform.
            # The production exact-byte write must bypass this text-only conversion.
            bootstrap = '''import pathlib,runpy,sys
original=pathlib.Path.write_text
peer_file=pathlib.Path(sys.argv[3]).resolve()
def windows_text(self,text,*args,**kwargs):
    if self == peer_file:
        return self.write_bytes(text.replace('\\n','\\r\\n').encode(kwargs.get('encoding') or 'utf-8'))
    return original(self,text,*args,**kwargs)
pathlib.Path.write_text=windows_text
script,manifest=sys.argv[1:3]
sys.argv=[script,'--manifest',manifest]
runpy.run_path(script,run_name='__main__')
'''
            request = {'op': 'peer-change', 'path': file.name, 'text': requested, 'message': 'nonoverlapping peer edit'}
            env = os.environ.copy()
            env['PYTHONDONTWRITEBYTECODE'] = '1'
            result = subprocess.run([sys.executable, '-c', bootstrap, str(script), str(manifest), str(peer / file.name)],
                                    input=json.dumps(request).encode('utf-8'), capture_output=True, env=env, timeout=60, check=True)
            self.assertEqual(json.loads(result.stdout)['head'], 2)
            self.assertEqual((peer / file.name).read_bytes(), requested.encode('utf-8'))
            self.assertEqual(svn('cat', repo.as_uri() + '/' + file.name + '@'), requested.encode('utf-8'))
            svn('update', str(wc) + '@')
            status = ET.fromstring(svn('status', '--xml', str(file) + '@')).find('.//wc-status')
            self.assertEqual(status.attrib['item'], 'modified')
            merged = file.read_bytes()
            self.assertTrue(merged.startswith(local[0].encode('utf-8')))
            self.assertTrue(merged.endswith(remote[-1].encode('utf-8')))
            self.assertNotIn(b'\r', merged)
            self.assertNotIn(b'<<<<<<<', merged)


if __name__ == '__main__':
    unittest.main()
