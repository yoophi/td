package testutil

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitHubJournalFixture exercises actual production client factories through gh
// subprocesses. Mutable native issues/comments and journals are independent.
type GitHubJournalFixture struct{ Directory, StatePath, RequestsPath string }

func NewGitHubJournalFixture(t *testing.T) GitHubJournalFixture {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".todos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".todos", "config.json"), []byte(`{"store":"gh-issue","github":{"remote":"origin","repo":"owner/repo"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	f := GitHubJournalFixture{Directory: dir, StatePath: filepath.Join(dir, "native.json"), RequestsPath: filepath.Join(dir, "requests.jsonl")}
	t.Setenv("TD_JOURNAL_NATIVE", f.StatePath)
	t.Setenv("TD_JOURNAL_REQUESTS", f.RequestsPath)
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_GH_CACHE", "off")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(journalGHScript), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

// GitHubJournalReceipt is the small set of independently observed receipt fields.
type GitHubJournalReceipt struct {
	Action       string `json:"action"`
	Phase        string `json:"phase"`
	SessionID    string `json:"session_id"`
	CarrierID    string `json:"carrier_id"`
	CommentAfter *struct {
		Author string `json:"author"`
	} `json:"comment_after"`
}

func (f GitHubJournalFixture) Journals(t *testing.T) []GitHubJournalReceipt {
	t.Helper()
	data, err := os.ReadFile(f.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	records := []GitHubJournalReceipt{}
	for _, c := range state.Comments {
		for _, marker := range []string{"<!-- td:action:v1\n", "<!-- td:action:v2\n"} {
			if index := strings.Index(c.Body, marker); index >= 0 {
				var a GitHubJournalReceipt
				if err := json.Unmarshal([]byte(c.Body[index+len(marker):len(c.Body)-len("\n-->")]), &a); err != nil {
					t.Fatal(err)
				}
				records = append(records, a)
			}
		}
	}
	return records
}

const journalGHScript = `#!/usr/bin/env python3
import sys,os,json,pathlib,datetime
args=sys.argv[1:]
if args[0]=='auth':
 if args[1]=='token': print('fixture-journal-token')
 sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo') or a=='user')
method=args[args.index('--method')+1] if '--method' in args else 'GET'
file=pathlib.Path(os.environ['TD_JOURNAL_NATIVE'])
def body(meta):return '<!-- td:issue:v1\n'+json.dumps(meta,separators=(',',':'))+'\n-->'
def issue(n,title,meta):return {'number':n,'title':title,'body':body(meta),'state':'open','labels':[],'updated_at':'2026-10-10T00:00:00Z','created_at':'2026-10-10T00:00:00Z','user':{'login':'fixture-author'}}
state=json.loads(file.read_text()) if file.exists() else {'issues':{'1':issue(1,'Original fixture',{'type':'task','priority':'P2','points':0}), '999':issue(999,'td action journal',{'entity_kind':'journal','type':'task','priority':'P2','points':0})},'comments':[],'tick':0}
with open(os.environ['TD_JOURNAL_REQUESTS'],'a') as log:log.write(json.dumps({'method':method,'path':path})+'\n')
def fail(message):sys.stderr.write(message);sys.exit(1)
def clock():
 state['tick']+=1
 return (datetime.datetime(2026,10,10,tzinfo=datetime.timezone.utc)+datetime.timedelta(seconds=state['tick'])).isoformat().replace('+00:00','Z')
def save():file.write_text(json.dumps(state))
payload=json.load(sys.stdin) if method in ['POST','PATCH'] else {}
mode=os.environ.get('TD_JOURNAL_FAIL','')
issues=state['issues'];comments=state['comments'];prefix='repos/owner/repo'
if path=='user':data={'login':'fixture-author'}
elif path==prefix:data={'full_name':'owner/repo','has_issues':True}
elif '/labels?' in path:data=[[{'name':'td:'+s} for s in ['open','in_progress','in_review','closed','blocked']]+[{'name':'td:priority:P'+str(n)} for n in range(5)]]
elif path.startswith(prefix+'/issues?'):data=[list(issues.values())]
elif path==prefix+'/issues/comments?per_page=100':data=[comments]
elif '/comments?per_page=100' in path:
 n=path.split('/issues/')[1].split('/')[0];data=[[c for c in comments if c['issue_url'].endswith('/'+n)]]
elif '/events?' in path:data=[[]]
elif path.startswith(prefix+'/issues/comments/'):
 n=int(path.rsplit('/',1)[1]);data=next((c for c in comments if c['id']==n),None)
 if data is None:fail('HTTP 404 comment missing')
 if method=='PATCH':
  if mode=='receipt-denied':fail('HTTP 403 journal receipt denied')
  data['body']=payload['body'];data['updated_at']=clock();save()
  if mode=='receipt-lost':fail('connection lost after journal receipt')
elif path.endswith('/comments') and method=='POST':
 n=path.split('/issues/')[1].split('/')[0]
 if n=='999' and mode=='intent-denied':fail('HTTP 403 journal intent denied')
 if n!='999' and mode=='comment-denied':fail('HTTP 403 native comment denied')
 stamp=clock();data={'id':100+len(comments),'body':payload['body'],'issue_url':'https://api.github.com/repos/owner/repo/issues/'+n,'user':{'login':'fixture-author'},'created_at':stamp,'updated_at':stamp};comments.append(data)
 issues[n]['updated_at']=stamp;save()
 if n!='999' and mode=='comment-lost':fail('connection lost after native comment')
elif path==prefix+'/issues' and method=='POST':
 if mode=='target-denied':fail('HTTP 403 target denied')
 n=str(max([int(n) for n in issues if n!='999'])+1)
 data=issue(int(n),payload['title'],{});data.update(payload);data['labels']=[{'name':n} for n in payload.get('labels',[])];data['updated_at']=clock();issues[n]=data;save()
elif path.startswith(prefix+'/issues/'):
 n=path.rsplit('/',1)[1]
 if n not in issues:fail('HTTP 404 issue missing')
 data=issues[n]
 if method=='PATCH':
  if mode=='target-denied':fail('HTTP 403 target denied')
  data.update(payload)
  if 'labels' in payload:data['labels']=[{'name':n} for n in payload['labels']]
  data['updated_at']=clock();save()
else:fail('unexpected endpoint '+path)
if not file.exists():save()
sys.stdout.write('HTTP/2 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
