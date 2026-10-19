#!/usr/bin/env python3
"""Read-only API contract inspection; session bootstrap is the sole optional POST."""
import argparse
import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request


def scrub(value):
    if isinstance(value, dict):
        return {key: ('[redacted]' if any(s in key.lower() for s in ('token', 'cookie', 'authorization')) else scrub(v))
                for key, v in value.items()}
    if isinstance(value, list):
        return [scrub(v) for v in value]
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default=os.environ.get('SVNTUI_WEB_URL'))
    parser.add_argument('--project-id', help='inspect one already registered project')
    parser.add_argument('--evidence', required=True, type=Path)
    args = parser.parse_args()
    if not args.url:
        parser.error('provide --url or SVNTUI_WEB_URL; never pass a token in CLI arguments')
    url = urllib.parse.urlsplit(args.url)
    host = url.hostname
    if host != 'localhost' and not ipaddress.ip_address(host).is_loopback:
        parser.error('URL must be loopback')
    if url.scheme != 'http' or url.username or url.password or url.query:
        parser.error('expected plain loopback HTTP URL with no credentials/query')
    origin = urllib.parse.urlunsplit((url.scheme,url.netloc,'','',''))
    token = os.environ.get('SVNTUI_STARTUP_TOKEN') or urllib.parse.parse_qs(url.fragment).get('token',[None])[0]
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    args.evidence.mkdir(parents=True,exist_ok=True)
    records = []

    def request(route, data=None, persist=True):
        req = urllib.request.Request(origin+'/api/v1'+route, data=json.dumps(data).encode() if data is not None else None,
                                     headers={'Origin':origin,'Content-Type':'application/json'})
        try:
            response = opener.open(req,timeout=30)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            payload = json.load(response)
            status = response.status
        assert set(('ok','data','error','exit_code')).issubset(payload), 'envelope keys missing'
        assert isinstance(payload['ok'], bool) and isinstance(payload['exit_code'], int), 'bad envelope types'
        records.append({'method':req.method,'path':route.split('?')[0],'status':status,'ok':payload['ok']})
        if persist:
            (args.evidence / f'{len(records):02d}-response.json').write_text(json.dumps(scrub(payload),ensure_ascii=False,indent=2),encoding="utf-8")
        assert status == 200 and payload['ok'], f'API read failed ({status})'
        return payload['data']

    if token:
        request('/session', {'token':token}, persist=False)
    session = request('/session', persist=False)
    assert isinstance(session['csrf_token'],str) and session['csrf_token'], 'session lacks CSRF'
    projects = request('/projects')
    assert isinstance(projects,list), 'project list must be array'
    jobs = request('/jobs')
    assert isinstance(jobs,list), 'jobs must be array'
    ids = [args.project_id] if args.project_id else [p['id'] for p in projects]
    inspected = []
    for project_id in ids:
        prefix = '/projects/'+urllib.parse.quote(project_id,safe='')
        status = request(prefix+'/status')
        assert isinstance(status['entries'],list), 'status entries must be array'
        for entry in status['entries']:
            assert set(('path','item','props','repos_item','repos_props','changelist','tree_conflicted','revision')).issubset(entry), 'status DTO fields'
        request(prefix+'/diff?target='+urllib.parse.quote('selected.txt',safe=''))
        page = request(prefix+'/history?limit=50')
        assert set(('entries','next_revision','has_more')).issubset(page), 'history cursor contract'
        seen = {e['revision'] for e in page['entries']}
        pages = 1
        while page['has_more'] and pages < 10:
            cursor = page['next_revision']
            page = request(prefix+'/history?limit=50&before_revision='+str(cursor))
            revisions = {e['revision'] for e in page['entries']}
            assert not revisions.intersection(seen), 'history page overlap'
            seen.update(revisions)
            pages += 1
        conflicts = request(prefix+'/conflicts')
        assert isinstance(conflicts,list), 'conflicts must be array'
        for conflict in conflicts:
            assert set(('path','kinds','base','mine','theirs','detail','accepts')).issubset(conflict), 'conflict DTO fields'
        draft = request(prefix+'/draft')
        assert set(('targets','message','revision')).issubset(draft), 'draft fields'
        points = request(prefix+'/safepoints')
        assert isinstance(points,list), 'SafePoints must be array'
        for point in points:
            request(prefix+'/safepoints/'+urllib.parse.quote(point['id'],safe=''))
        request(prefix+'/operations')
        inspected.append({'id':project_id,'entries':len(status['entries']),'conflicts':len(conflicts),
                          'history_pages':pages,'history_revisions':len(seen)})
    report={'api_reads_only':True,'browser_workflows_executed':False,'version':session.get('version'),
            'requests':records,'projects':inspected}
    (args.evidence/'api-summary.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding="utf-8")
    print(json.dumps(report,ensure_ascii=True,indent=2))


if __name__ == '__main__':
    main()
