package cmd

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Existing command fixtures model a repository with a separate journal carrier.
// Its writes are persisted independently of ordinary issue/comment payloads.
func githubJournalFixtureScript(t *testing.T, script string) string {
	t.Helper()
	file, err := json.Marshal(filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var filename string
	if err := json.Unmarshal(file, &filename); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_GH_FIXTURE_JOURNAL", filename)
	prelude := `
import datetime,io
journal_file=pathlib.Path(` + string(file) + `)
journal=json.loads(journal_file.read_text()) if journal_file.exists() else {'comments':[]}
def fixture_tick(value):return (datetime.datetime.fromisoformat(value.replace('Z','+00:00'))+datetime.timedelta(seconds=1)).isoformat().replace('+00:00','Z')
def fixture_ordered_record(body):
 marker='<!-- td:action:v14\n';start=body.find(marker)
 return json.loads(body[start+len(marker):body.rindex('\n-->')]) if start>=0 else None
journal_carrier={'number':999,'title':'td action journal','state':'open','labels':[],'updated_at':'2026-10-09T00:00:00Z','body':'<!-- td:issue:v1\n'+json.dumps({'entity_kind':'journal','type':'task','priority':'P2','points':0})+'\n-->'}
journal_carrier=journal.get('carrier',None if os.environ.get('TD_GH_FIXTURE_NO_CARRIER') else journal_carrier)
def journal_result(data):
 print('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data));sys.exit(0)
def with_journal_observation(path,data):
 if path.startswith('repos/owner/repo/issues?') and journal_carrier is not None:
  data[0].append(journal_carrier)
 if path=='repos/owner/repo/issues/comments?per_page=100':
  data[0].extend(journal['comments'])
 if os.environ.get('TD_GH_FIXTURE_NATIVE_EVENTS')=='1':
  states=journal.setdefault('native_states',{});events=journal.setdefault('native_events',{});changed=False
  if isinstance(data,dict) and 'number' in data and 'state' in data and data['number']!=999:
   key=str(data['number']);state=data['state']
   if key not in states:events[key]=([{'id':1,'event':'closed'}] if state=='closed' else []);changed=True
   elif states[key]!=state:events[key].append({'id':len(events[key])+1,'event':'closed' if state=='closed' else 'reopened'});changed=True
   states[key]=state
  if '/events?' in path:
   key=path.split('/issues/')[1].split('/')[0];data=[events.get(key,[])]
  if changed:journal_file.write_text(json.dumps(journal))
 return data
if path=='repos/owner/repo/issues/999':
 if journal_carrier is None:sys.stderr.write('HTTP 404 journal carrier missing');sys.exit(1)
 journal_result(journal_carrier)
if path=='repos/owner/repo/issues' and method=='POST':
 payload=json.load(sys.stdin)
 if '"entity_kind":"journal"' in payload.get('body',''):
  journal_carrier=dict(payload,number=999,state='open',labels=[],updated_at='2026-10-09T00:00:00Z',user={'login':'fixture-author'})
  journal['carrier']=journal_carrier;journal_file.write_text(json.dumps(journal));journal_result(journal_carrier)
 sys.stdin=io.StringIO(json.dumps(payload))
if path=='user':journal_result({'login':'fixture-author'})
if path=='repos/owner/repo/issues/999/comments' and method=='POST':
 if os.environ.get('TD_GH_JOURNAL_FAIL')=='intent-denied':sys.stderr.write('HTTP 403 journal intent denied');sys.exit(1)
 body=json.load(sys.stdin)['body'];n=1000+len(journal['comments'])
 data={'id':n,'body':body,'issue_url':'https://api.github.com/repos/owner/repo/issues/999','created_at':('2026-10-09T00:00:00Z' if not journal['comments'] else fixture_tick(journal['comments'][-1]['created_at'])),'user':{'login':'fixture-author'}}
 data['updated_at']=data['created_at'];journal['comments'].append(data);journal_file.write_text(json.dumps(journal))
 if os.environ.get('TD_GH_JOURNAL_FAIL')=='intent-lost':sys.stderr.write('connection lost after intent');sys.exit(1)
 journal_result(data)
if path.startswith('repos/owner/repo/issues/comments/'):
 n=int(path.rsplit('/',1)[1]);data=next((c for c in journal['comments'] if c['id']==n),None)
 if data is not None:
  if method=='PATCH':
   payload=json.load(sys.stdin)
   ordered=fixture_ordered_record(payload.get('body',''));steps=ordered.get('group',{}).get('ordered',{}).get('steps',[]) if ordered else [];step=steps[-1] if steps else None
   completes_ordered_step=step is not None and step.get('result') is not None
   if os.environ.get('TD_GH_JOURNAL_FAIL')=='receipt-denied' or (os.environ.get('TD_GH_JOURNAL_FAIL')=='undo-completion-denied' and ('"phase":"undone"' in payload.get('body','') or completes_ordered_step)):sys.stderr.write('HTTP 403 journal receipt denied');sys.exit(1)
   data.update(payload);data['updated_at']=fixture_tick(data['updated_at'])
   target=os.environ.get('TD_GH_GROUP_EVENT_TARGET')
   ordered_native_input=ordered is not None and ordered.get('phase')=='undo_prepared' and step is not None and step.get('result') is None and step['source']['state']!=step['input']['state']
   legacy_native_input='"group_operation_id":' in payload.get('body','') and '"native_undo":' in payload.get('body','') and '"phase":"undo_prepared"' in payload.get('body','')
   if target and (legacy_native_input or ordered_native_input) and not journal.get('foreign_group_event_injected'):
    observed=journal['native_events'][target];n=observed[-1]['id'] if observed else 0;observed.extend([{'id':n+1,'event':'reopened'},{'id':n+2,'event':'closed'}]);journal['foreign_group_event_injected']=True
   journal_file.write_text(json.dumps(journal))
   if os.environ.get('TD_GH_JOURNAL_FAIL')=='receipt-lost':sys.stderr.write('connection lost after receipt');sys.exit(1)
  journal_result(data)
`
	needle := "f=pathlib.Path("
	if !strings.Contains(script, needle) {
		t.Fatal("fixture insertion point missing")
	}
	script = strings.Replace(script, needle, prelude+"\n"+needle, 1)
	needle = "sys.stdout.write('HTTP/2.0 200 OK"
	if !strings.Contains(script, needle) {
		t.Fatal("fixture observation point missing")
	}
	return strings.Replace(script, needle, "data=with_journal_observation(path,data)\n"+needle, 1)
}

func githubSingleIssueJournalScript(t *testing.T) string {
	t.Helper()
	return githubJournalFixtureScript(t, `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo') or a=='user');method=args[args.index('--method')+1] if '--method' in args else 'GET'
if os.environ.get('TD_TEST_REQUESTS'):
 with open(os.environ['TD_TEST_REQUESTS'],'a') as log:log.write(method+' '+path+'\n')
f=pathlib.Path(os.environ['TD_TEST_ISSUE'])
activities_file=f.with_suffix('.comments.json');activities=json.loads(activities_file.read_text()) if activities_file.exists() else []
issue=json.loads(f.read_text());issue.setdefault('updated_at','2026-10-09T00:00:00Z');issue.setdefault('body','');issue.setdefault('labels',[]);issue.setdefault('user',{'login':'fixture-author'})
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif path.startswith('repos/owner/repo/issues?'):data=[[issue]]
elif '/labels?' in path:data=[[{'name':'td:'+v} for v in ['open','in_progress','blocked','in_review','closed']]+[{'name':'td:priority:P'+str(v)} for v in range(5)]]
elif '/events?' in path:data=[[{'id':1,'event':'closed'}]] if issue['state']=='closed' else [[]]
elif '/comments?' in path:data=[activities]
elif path.startswith('repos/owner/repo/issues/comments/') and method=='GET':
 n=int(path.rsplit('/',1)[1]);data=next((c for c in activities if c['id']==n),None)
 if data is None:sys.stderr.write('HTTP 404 comment missing');sys.exit(1)
elif path=='repos/owner/repo/issues/1/comments' and method=='POST':
 if os.environ.get('TD_TEST_COMMENT_FAIL')=='yes':sys.stderr.write('forbidden');sys.exit(1)
 data={'id':200+len(activities),'body':json.load(sys.stdin)['body'],'issue_url':'https://api.github.com/repos/owner/repo/issues/1','created_at':'2026-10-09T00:00:00Z','updated_at':'2026-10-09T00:00:00Z','user':{'login':'fixture-author'}}
 activities.append(data);activities_file.write_text(json.dumps(activities));issue['updated_at']=fixture_tick(issue['updated_at']);f.write_text(json.dumps(issue))
elif path=='repos/owner/repo/issues/1':
 if method=='PATCH':
  patch=json.load(sys.stdin)
  if 'labels' in patch:patch['labels']=[{'name':v} for v in patch['labels']]
  issue.update(patch);issue['updated_at']=fixture_tick(issue['updated_at']);f.write_text(json.dumps(issue))
 data=issue
else:sys.stderr.write('unexpected endpoint '+path);sys.exit(1)
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`)
}
