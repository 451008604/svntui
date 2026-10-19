#!/usr/bin/env python3
"""Native SVN fixture setup/postconditions; JSON stdin/stdout, no product UI mutation."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import urllib.parse
import xml.etree.ElementTree as ET


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest',required=True,type=Path)
    args=parser.parse_args()
    manifest=json.loads(args.manifest.read_text(encoding='utf-8'))
    request=json.load(sys.stdin)
    wc=Path(manifest['wc']).resolve()
    peer=Path(manifest['peer']).resolve()
    evidence=Path(manifest['evidence'])
    commands=[]

    def run(*argv,cwd=None):
        result=subprocess.run([str(a) for a in argv],capture_output=True,timeout=60,cwd=cwd,
                              creationflags=subprocess.CREATE_NO_WINDOW if os.name=='nt' else 0)
        commands.append({'args':[str(a) for a in argv],'exit_code':result.returncode,
                         'stdout_sha256':hashlib.sha256(result.stdout).hexdigest(),
                         'stderr':result.stderr.decode('utf-8','replace')})
        if result.returncode:
            raise RuntimeError('native SVN command failed: '+result.stderr.decode('utf-8','replace'))
        return result.stdout

    def svn(*argv,cwd=None):
        return run(os.environ.get('SVNTUI_SVN','svn'),*argv,'--non-interactive','--config-dir',manifest['svn_config'],cwd=cwd)

    def target(name,root=wc):
        rel=Path(name)
        if rel.is_absolute() or '.svn' in rel.parts:
            raise ValueError('fixture relative path required')
        result=(root/rel).resolve()
        result.relative_to(root)
        return result

    def status():
        raw=svn('status','--xml','--no-ignore',str(wc)+'@')
        root=ET.fromstring(raw)
        entries={}
        for entry in root.findall('.//entry'):
            name=str(Path(entry.attrib['path']).relative_to(wc)).replace('\\','/')
            entries[name]=dict(entry.find('wc-status').attrib)
        groups={node.attrib['name']:[str(Path(e.attrib['path']).relative_to(wc)).replace('\\','/') for e in node.findall('entry')]
                for node in root.findall('changelist')}
        return {'entries':entries,'changelists':groups,
                'head':int(svn('info','--show-item','revision',manifest['repo_url']).strip()),
                'wc_revision':int(svn('info','--show-item','revision',str(wc)+'@').strip())}

    op=request['op']
    try:
        if op=='status':
            result=status()
        elif op in ('read','cat'):
            if op=='read':
                data=target(request['path']).read_bytes()
            else:
                url=manifest['repo_url'].rstrip('/')+'/'+urllib.parse.quote(request['path'],safe='/')+'@'
                data=svn('cat','--revision',str(request.get('revision','HEAD')),url)
            result={'sha256':hashlib.sha256(data).hexdigest(),'base64':base64.b64encode(data).decode(),
                    'text':data.decode('utf-8','replace')}
        elif op in ('write','append'):
            file=target(request['path'])
            file.parent.mkdir(parents=True,exist_ok=True)
            data=base64.b64decode(request['base64']) if 'base64' in request else request['text'].encode()
            with file.open('ab' if op=='append' else 'wb') as out:
                out.write(data)
            result={'path':request['path'],'sha256':hashlib.sha256(file.read_bytes()).hexdigest()}
        elif op in ('propset','propget'):
            file=target(request['path'])
            if op=='propset':
                svn('propset',request['name'],request['value'],str(file)+'@')
                result={'path':request['path'],'property':request['name']}
            else:
                result={'value':svn('propget','--strict',request['name'],str(file)+'@').decode('utf-8')}
        elif op=='diff-bytes':
            # A working-copy diff does not parse peg revisions; keep its path literal.
            raw=run(os.environ.get('SVNTUI_SVN','svn'),'diff','--git','--non-interactive',
                    '--config-dir',manifest['svn_config'],'--',request['path'],cwd=wc)
            result={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
        elif op=='peer-change':
            svn('update',str(peer)+'@')
            file=target(request['path'],peer)
            existed=file.exists()
            file.parent.mkdir(parents=True,exist_ok=True)
            file.write_bytes(request['text'].encode('utf-8'))
            if not existed:
                svn('add',str(file)+'@')
            svn('commit','--message',request['message'],'--username','browser-peer',str(file)+'@')
            result={'head':int(svn('info','--show-item','revision',manifest['repo_url']).strip()),'path':request['path']}
        elif op=='log':
            raw=svn('log','--xml','--verbose','--revision',str(request.get('revision','HEAD')),manifest['repo_url'])
            entry=ET.fromstring(raw).find('logentry')
            result={'revision':int(entry.attrib['revision']),'author':entry.findtext('author'),
                    'date':entry.findtext('date'),'message':entry.findtext('msg'),
                    'paths':[{'path':p.text,**p.attrib} for p in entry.findall('./paths/path')]}
        else:
            raise ValueError('unknown native operation')
        print(json.dumps(result,ensure_ascii=True))
    finally:
        (evidence/f'svn-native-{time.time_ns()}.json').write_text(json.dumps({'operation':op,'commands':commands},ensure_ascii=False,indent=2),encoding='utf-8')


if __name__=='__main__':
    main()
