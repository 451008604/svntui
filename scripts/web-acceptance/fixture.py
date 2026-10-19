#!/usr/bin/env python3
"""Create isolated real SVN fixtures; verify never mutates an existing fixture."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import xml.etree.ElementTree as ET


def peg(path):
    return str(path) + '@'


class Fixture:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.repo, self.wc, self.peer = (self.root / n for n in ('repo', 'wc', 'peer'))
        self.config = self.root / 'svn-config'
        self.evidence = self.root / 'evidence'
        self.evidence.mkdir(parents=True, exist_ok=True)
        self.config.mkdir(exist_ok=True)
        self.counter = 0

    def run(self, args, cwd=None):
        self.counter += 1
        result = subprocess.run([str(a) for a in args], cwd=cwd, capture_output=True, timeout=90)
        record = {'args': [str(a) for a in args], 'cwd': str(cwd) if cwd else None,
                  'exit_code': result.returncode, 'stdout': result.stdout.decode('utf-8', 'replace'),
                  'stderr': result.stderr.decode('utf-8', 'replace')}
        (self.evidence / f'{self.counter:04d}-command.json').write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding="utf-8")
        if result.returncode:
            raise RuntimeError(f'command failed: {args[0:2]}: {record["stderr"]}')
        return result.stdout

    def svn(self, *args, cwd=None):
        return self.run([shutil.which('svn'), *args, '--non-interactive', '--config-dir', self.config], cwd)

    def write(self, name, content, wc=None):
        path = (wc or self.wc) / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content if isinstance(content, bytes) else content.encode())

    def commit(self, message, username='fixture-author', wc=None):
        return self.svn('commit', '--message', message, '--username', username, peg(wc or self.wc))

    def prepare(self, revisions=125, large_bytes=1400000, mode="full"):
        self.run([shutil.which('svnadmin'), 'create', self.repo])
        self.svn('checkout', self.repo.as_uri(), self.wc)
        baseline = {
            'selected.txt': 'selected baseline\n', 'unselected.txt': 'unselected baseline\n',
            '中文 文件.txt': '中文基线\n', 'space name.txt': 'space baseline\n',
            'foo': 'plain foo\n', 'foo@': 'at foo\n', 'asset@2x.png': b'\x89PNG\r\n\x1a\n\x00fixture\xff',
            'binary.bin': bytes(range(256)), 'nested/first.txt': 'first baseline\n',
            'nested/second.txt': 'second baseline\n',
            'nested/中文 foo@ name.txt': 'nested special baseline\n',
            'update-target.txt': ''.join(f'update line {i:02d} baseline\n' for i in range(1,21)),
            'text-conflict.txt': 'same line\n',
            'prop-conflict.txt': 'property baseline\n', 'tree-conflict.txt': 'tree baseline\n',
            'history.txt': 'history base\n', 'delete-me.txt': 'delete baseline\n',
            'move-me.txt': 'move baseline\n', 'delete-clean.txt': 'clean delete baseline\n', 'revert-me.txt': 'revert baseline\n',
        }
        if large_bytes:
            baseline['large.txt'] = ('baseline line\n' * (large_bytes // 14 + 1))[:large_bytes]
        for name, data in baseline.items():
            self.write(name, data)
        self.svn('add', '--force', peg(self.wc))
        self.svn('propset', 'svn:mime-type', 'application/octet-stream', peg(self.wc / 'binary.bin'))
        self.svn('propset', 'svn:mime-type', 'application/octet-stream', peg(self.wc / 'asset@2x.png'))
        self.svn('propset', 'fixture:color', 'base', peg(self.wc / 'prop-conflict.txt'))
        self.commit('fixture baseline 中文 & apostrophe\' <img src=x onerror=window.__svntui_injected=1>')
        # A real revision per commit ensures page cursors have more than 120 rows.
        mucc = shutil.which('svnmucc')
        for revision in range(2, max(2, revisions + (mode == "basic"))):
            message = f'history {revision:03d} ' + ('needle 中文' if revision % 10 == 0 else 'ordinary')
            author = f'history-{revision % 2}'
            if mucc:
                # URL commits avoid one filesystem timestamp wait per WC commit.
                seed = self.root / 'history-seed.txt'
                seed.write_text(f'history revision {revision}\n', encoding='utf-8')
                self.run([mucc, '--root-url', self.repo.as_uri(), '--message', message,
                          '--username', author, '--non-interactive', '--config-dir', self.config,
                          'put', seed, 'history.txt'])
            else:
                self.write('history.txt', f'history revision {revision}\n')
                self.commit(message, author)
        self.svn('update', peg(self.wc))
        self.svn('checkout', self.repo.as_uri(), self.peer)
        if mode == "full":
            # Local and peer edits deliberately collide on text, property, and deletion.
            self.write('text-conflict.txt', 'local text\n')
            self.svn('propset', 'fixture:color', 'local', peg(self.wc / 'prop-conflict.txt'))
            self.write('tree-conflict.txt', 'local edited before remote deletion\n')
            self.write('text-conflict.txt', 'remote text\n', self.peer)
            self.svn('propset', 'fixture:color', 'remote', peg(self.peer / 'prop-conflict.txt'))
            self.svn('delete', peg(self.peer / 'tree-conflict.txt'))
            self.commit('peer creates text property tree conflicts', 'peer-author', self.peer)
            self.svn('update', '--accept', 'postpone', peg(self.wc))
        for name in ('selected.txt', 'unselected.txt', '中文 文件.txt', 'space name.txt', 'foo', 'foo@',
                     'nested/first.txt', 'nested/second.txt', 'nested/中文 foo@ name.txt', 'revert-me.txt'):
            self.write(name, baseline[name] + 'local modification 中文\n')
        self.write('binary.bin', bytes(reversed(range(256))))
        self.write('asset@2x.png', b'\x89PNG\r\n\x1a\n\x00changed\xff')
        if large_bytes:
            self.write('large.txt', ('changed line\n' * (large_bytes // 13 + 1))[:large_bytes])
        self.write('untracked.txt', 'unversioned preview\n')
        self.write('未跟踪 空格.txt', '未版本化中文\n')
        self.write("ampersand & apostrophe'.txt", 'safe legal filename\n')
        markup = '<img src=x onerror=window.__svntui_injected=1>.txt'
        if os.name != 'nt':
            self.write(markup, 'render this path as literal text\n')
        self.write('nested/added child.txt', 'scheduled child, never auto select\n')
        self.svn('add', peg(self.wc / 'nested/added child.txt'))
        self.svn('changelist', '验收 列表', peg(self.wc / 'selected.txt'), peg(self.wc / 'space name.txt'))
        outside = self.root / 'outside'
        outside.mkdir()
        (outside / 'secret.txt').write_text('outside fixture sentinel\n', encoding='utf-8')
        symlink = {'available': False, 'reason': ''}
        try:
            (self.wc / 'escape-link').symlink_to(outside, target_is_directory=True)
            symlink['available'] = True
        except OSError as exc:
            symlink['reason'] = type(exc).__name__ + ': ' + str(exc)
        for name in ('config', 'state', 'cache'):
            (self.root / name).mkdir()
        manifest = {
            'format': 2, 'mode': mode, 'requested_revisions': revisions, 'root': str(self.root), 'repo': str(self.repo), 'repo_url': self.repo.as_uri(),
            'wc': str(self.wc), 'peer': str(self.peer), 'svn_config': str(self.config),
            'evidence': str(self.evidence), 'env': {
                'XDG_CONFIG_HOME': str(self.root / 'config'),
                'XDG_STATE_HOME': str(self.root / 'state'),
                'XDG_CACHE_HOME': str(self.root / 'cache')},
            'selected': ['selected.txt'], 'unselected': ['unselected.txt', 'nested/second.txt'],
            'commit_target': 'nested/中文 foo@ name.txt',
            'changelist': '验收 列表', 'conflicts': {
                'text': 'text-conflict.txt', 'property': 'prop-conflict.txt', 'tree': 'tree-conflict.txt'} if mode == 'full' else {},
            'markup_path': markup if os.name != 'nt' else None,
            'markup_message': '<img src=x onerror=window.__svntui_injected=1>',
            'symlink': symlink, 'large_bytes': large_bytes,
        }
        (self.root / 'fixture.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        return manifest


def verify(manifest):
    wc = Path(manifest['wc'])
    args = ['svn', 'status', '--xml', '--no-ignore', '--non-interactive', '--config-dir', manifest['svn_config'], peg(wc)]
    raw = subprocess.check_output(args, timeout=30)
    entries = {}
    for node in ET.fromstring(raw).findall('.//entry'):
        status = node.find('wc-status')
        rel = str(Path(node.attrib['path']).relative_to(wc))
        entries[rel] = dict(status.attrib)
    def check(condition, message):
        if not condition:
            raise AssertionError(message)
    for name in ('中文 文件.txt', 'space name.txt', 'foo', 'foo@', 'asset@2x.png', 'binary.bin'):
        check(entries[name]['item'] == 'modified', name + ' must be modified')
    check(entries['untracked.txt']['item'] == 'unversioned', 'untracked file')
    check(entries['nested/added child.txt']['item'] == 'added', 'scheduled child')
    mode = manifest.get('mode', 'full')
    conflicts = {name: status for name, status in entries.items()
                 if status.get('item') == 'conflicted' or status.get('props') == 'conflicted'
                 or status.get('tree-conflicted') == 'true'}
    if mode == 'full':
        check(entries['text-conflict.txt']['item'] == 'conflicted', 'real text conflict missing')
        check(entries['prop-conflict.txt']['props'] == 'conflicted', 'real property conflict missing')
        check(entries['tree-conflict.txt']['tree-conflicted'] == 'true', 'real tree conflict missing')
    else:
        check(not conflicts, 'basic mode must have zero SVN conflicts')
        check(not (wc / 'large.txt').exists() if not manifest['large_bytes'] else True, 'large diff must be omitted')
    changelists = {node.attrib['name']: [str(Path(e.attrib['path']).relative_to(wc)) for e in node.findall('entry')]
                  for node in ET.fromstring(raw).findall('changelist')}
    check(set(changelists[manifest['changelist']]) == {'selected.txt', 'space name.txt'}, 'changelist paths')
    logs = ET.fromstring(subprocess.check_output(['svn', 'log', '--xml', '--limit', '1000', manifest['repo_url'],
                          '--non-interactive', '--config-dir', manifest['svn_config']], timeout=30))
    revision_count = len(logs.findall('logentry'))
    report = {'fixture_only': True, 'mode': mode, 'browser_acceptance_executed': False,
              'conflict_count': len(conflicts),
              'revision_count': revision_count, 'head_revision': int(logs.find('logentry').attrib['revision']),
              'history_over_120': revision_count > 120, 'status_entries': entries,
              'changelists': changelists, 'real_conflicts': list(manifest['conflicts']),
              'binary_is_non_utf8': b'\xff' in (wc / 'binary.bin').read_bytes(), 'symlink': manifest['symlink']}
    evidence = Path(manifest['evidence'])
    (evidence / 'status.xml').write_bytes(raw)
    (evidence / 'fixture-verification.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, help='new empty fixture root (default: unique temporary directory)')
    parser.add_argument('--revisions', type=int, default=125)
    parser.add_argument('--mode', choices=('full','basic'), default='full', help='basic omits all three conflict setups')
    parser.add_argument('--large-bytes', type=int, help='default full=1400000, basic=0; zero omits large.txt')
    parser.add_argument('--verify', type=Path, metavar='FIXTURE_JSON', help='only inspect a previously created fixture')
    opts = parser.parse_args()
    started = time.monotonic()
    if opts.verify:
        manifest = json.loads(opts.verify.read_text(encoding="utf-8"))
    else:
        if opts.root:
            if opts.root.exists() and any(opts.root.iterdir()):
                parser.error('--root must be absent or empty')
            opts.root.mkdir(parents=True, exist_ok=True)
        root = opts.root or Path(tempfile.mkdtemp(prefix='svntui-web-acceptance-'))
        large_bytes = opts.large_bytes if opts.large_bytes is not None else (1400000 if opts.mode == 'full' else 0)
        if large_bytes < 0 or opts.revisions < 2:
            parser.error('--large-bytes must be nonnegative and --revisions at least 2')
        manifest = Fixture(root).prepare(opts.revisions, large_bytes, opts.mode)
    report = verify(manifest)
    print(json.dumps({'manifest': str(Path(manifest['root']) / 'fixture.json'),
                      'mode': report['mode'], 'conflict_count': report['conflict_count'],
                      'head_revision': report['head_revision'], 'real_conflicts': report['real_conflicts'],
                      'history_over_120': report['history_over_120'],
                      'elapsed_seconds': round(time.monotonic() - started, 2)}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
