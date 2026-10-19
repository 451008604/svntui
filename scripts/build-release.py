#!/usr/bin/env python3
"""Build reproducible platform packages from a clean, accepted source checkout."""

from pathlib import Path
import argparse
import gzip
import hashlib
import io
import json
import os
import re
import subprocess
import tarfile
import zipfile


def run(*args, cwd, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    options = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    if run('git', 'status', '--porcelain', cwd=repo):
        parser.error('工作区存在未提交更改；验收并提交后再构建发行包')
    match = re.search(r'const version = "([0-9]+\.[0-9]+\.[0-9]+)"', (repo / 'cmd/svntui/main.go').read_text())
    if not match:
        parser.error('无法读取 svntui 版本号')
    version = match.group(1)
    commit = run('git', 'rev-parse', 'HEAD', cwd=repo)
    output = options.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    tracked = run('git', 'ls-files', cwd=repo).splitlines()
    sources = {name: hashlib.sha256((repo / name).read_bytes()).hexdigest() for name in tracked}
    assets = {}
    for path in sorted((repo / 'internal/webui/dist').rglob('*')):
        if path.is_file():
            assets[str(path.relative_to(repo))] = hashlib.sha256(path.read_bytes()).hexdigest()
    if not assets or 'internal/webui/dist/index.html' not in assets:
        parser.error('缺少前端构建资源；请先执行 web 目录下 npm ci 和 npm run build')

    notices = repo / 'internal/webui/dist/THIRD_PARTY_LICENSES.md'
    if not notices.is_file():
        parser.error('缺少前端依赖许可说明；请重新构建前端资源')
    docs = {'README.md': (repo / 'README.md').read_bytes(), 'LICENSE': (repo / 'LICENSE').read_bytes(),
            'THIRD_PARTY_LICENSES.md': notices.read_bytes()}
    for path in sorted((repo / 'docs').rglob('*.md')):
        if 'plans' not in path.relative_to(repo / 'docs').parts:
            docs[str(path.relative_to(repo))] = path.read_bytes()
    records = []
    # 三个平台复用同一源码、前端资源和离线文档。
    for label, goos, arch in [('macos-arm64', 'darwin', 'arm64'), ('linux-amd64', 'linux', 'amd64'), ('windows-amd64', 'windows', 'amd64')]:
        prefix = f'svntui-v{version}-{label}'
        directory = output / prefix
        directory.mkdir(exist_ok=True)
        executable = 'svntui.exe' if goos == 'windows' else 'svntui'
        env = dict(os.environ, GOOS=goos, GOARCH=arch, CGO_ENABLED='0', GOAMD64='v1', GOFLAGS='')
        subprocess.run(['go', 'build', '-buildvcs=false', '-trimpath', '-ldflags=-s -w', '-o', str(directory / executable), './cmd/svntui'], cwd=repo, env=env, check=True)
        files = dict(docs, **{executable: (directory / executable).read_bytes()})
        for name, data in docs.items():
            path = directory / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        suffix = '.zip' if goos == 'windows' else '.tar.gz'
        package = output / (prefix + suffix)
        if goos == 'windows':
            with zipfile.ZipFile(package, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
                for name, data in sorted(files.items()):
                    item = zipfile.ZipInfo(prefix + '/' + name, (2026, 1, 1, 0, 0, 0))
                    item.create_system = 3
                    item.external_attr = (0o100755 if name == executable else 0o100644) << 16
                    item.compress_type = zipfile.ZIP_DEFLATED
                    archive.writestr(item, data)
            with zipfile.ZipFile(package) as archive:
                assert archive.testzip() is None
                assert archive.read(prefix + '/' + executable) == files[executable]
        else:
            with package.open('wb') as raw:
                with gzip.GzipFile(filename='', mode='wb', fileobj=raw, mtime=0) as compressed:
                    with tarfile.open(fileobj=compressed, mode='w', format=tarfile.PAX_FORMAT) as archive:
                        for name, data in sorted(files.items()):
                            item = tarfile.TarInfo(prefix + '/' + name)
                            item.size = len(data)
                            item.mode = 0o755 if name == executable else 0o644
                            archive.addfile(item, io.BytesIO(data))
            with tarfile.open(package) as archive:
                assert archive.extractfile(prefix + '/' + executable).read() == files[executable]
                assert archive.getmember(prefix + '/' + executable).mode == 0o755
        records.append({'name': package.name, 'bytes': package.stat().st_size,
                        'sha256': hashlib.sha256(package.read_bytes()).hexdigest(),
                        'binary_sha256': hashlib.sha256(files[executable]).hexdigest()})
    # 清单同时覆盖所有编译输入，包含嵌入的前端产物。
    manifest = {'version': version, 'commit': commit, 'sources': sources, 'embedded_assets': assets,
                'go_toolchain': run('go', 'version', cwd=repo), 'packages': records}
    (output / 'build-manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
    (output / 'SHA256SUMS.txt').write_text(''.join(record['sha256'] + '  ' + record['name'] + '\n' for record in records))
    print(json.dumps({'version': version, 'commit': commit, 'packages': records}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
