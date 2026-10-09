# td-gui와 GitHub 저장소 호환 조사

2026-10-10. 설치된 td-gui는 v0.18.8이며 코드를 변경하지 않았다. 조사한 upstream main은 a9aefa5, 설치 버전 source tag v0.18.8도 비교했다.

## 현재 결론

기동과 확인한 조회 기능에 td-gui fork는 필요하지 않다. td 개발 빌드의 version 형식을 실제 기반 SemVer + development metadata로 바꾸면 기존 td-gui가 실행된다. 모든 mutation/board 계약 검증이 끝났다는 뜻은 아니다.

`td serve`는 API 서버이고 `td-gui`가 별도 포트에서 웹 화면을 제공한다. 따라서 td serve의 `/`에서 unsupported_operation이 나와도 GUI 연동을 판단할 수 없다. GUI가 출력하는 URL을 사용한다. `website/`는 문서 사이트다.

## 재현과 검증

- 설치 td: `td version devel+feat-gh-all-tasks.d5cedaa`.
- `td-gui --no-open --work-dir /Users/yoophi/project/td-gh-issue`: exit 1, `no version found in "td version devel+feat-gh-all-tasks.d5cedaa"`.
- td-gui ParseVersion은 숫자 X.Y.Z를 추출하고 최소 v0.57.0을 검사한다. 설치 버전과 조사한 main에서 동일한 원인이다.
- td checkout의 가장 가까운 조상 tag는 v0.66.0. `/tmp/td-tdgui-compat`을 `-X main.Version=v0.66.0+devel.feat-gh-all-tasks.b935df4.dirty`로 빌드하여 같은 td-gui의 `--td`에 전달했다. 이 표기는 release를 사칭하지 않고 개발 build metadata를 유지한다.
- 기존 td-gui v0.18.8이 `127.0.0.1:7777`에서 실행됐으며 자체 token을 사용하는 td serve를 시작했다. 브라우저에서 GitHub 작업 목록, gh-47 상세, description/acceptance/의존성을 확인했다.
- `/gui/about`, `/gui/query?q=priority = P1`, 필드 없는 `PATCH /v1/issues/gh-47 {}`가 성공했다. PATCH는 no-op이며 issue 수정 필드를 보내지 않았다.
- 시작 오류의 원인은 SemVer가 없는 개발 버전 출력이다. PATH와 td-gui binary 변경 없이 `--td`로 선택한 SemVer development build가 gate를 통과했다.

이 테스트 binary는 production install을 대체하지 않는다. 기본 `/opt/homebrew/bin/td`는 조사 시 여전히 d5cedaa development version이었고, 영구 installer 개선은 #54다. 새 GUI와 기존 8080 서버는 별도 프로세스다. port file이 가리키는 listener가 GUI의 자동 발견 대상이다.

## 수정 없이 가능한 범위와 한계

| 범위 | 근거/상태 |
|---|---|
| 시작 | SemVer development build를 지정하면 같은 GUI가 성공 |
| 작업 목록/상세/의존성 표시 | 실제 Chrome 화면에서 확인 |
| TDQ priority 검색 | GUI CLI proxy 결과에 gh-N IDs 확인 |
| API 수정 요청의 형식 | 필드 없는 PATCH가 no-op 성공, 실제 mutation E2E는 #55 |
| CRUD/workflow/comments/dependencies/boards | API 존재와 source 계약 비교, 실제 GUI/sample mutation 전체 검증은 #55 |
| SSE | 화면 connected 및 새 task 표시 관찰, 실패/재접속/비용 회귀 범위는 후속 검증 |
| 편집 시점부터의 stale draft 감지 | GUI의 If-Match 전송 지원이 필요하며 선택적인 upstream 개선 후보 |

현재 td API의 `If-Match` 검사는 **선택적**이다. GUI가 헤더를 보내지 않아도 요청은 수락되며, ghstore의 mutation 직전 원격 관찰 및 업데이트 사이 충돌 검사는 유지된다. 헤더가 없으면 사용자가 편집 화면을 연 이후의 모든 변경을 감지한다고 주장할 수 없다. 이것은 현재 GUI 기동의 필수 fork 사유가 아니다.

먼저 td의 정상적인 version/API 계약을 개선한다. GUI 변경이 실제로 필수인 기능이 확인되면 해당 한계와 upstream 개선 또는 fork 필요성을 별도로 기록한다. API와 CLI의 review attribution 및 metadata/native 상태 기준을 GUI 호환 때문에 약화시키지 않는다.

## 등록 작업

- [#53 기존 td-gui와 GitHub 저장소 연동](https://github.com/yoophi/td/issues/53)
- [#54 개발 빌드 SemVer 출력](https://github.com/yoophi/td/issues/54)
- [#55 GUI API/CLI 계약 및 브라우저 검증](https://github.com/yoophi/td/issues/55)

기존 조회 최적화 #47, 리뷰 표시 #48, 캐시 #52와 연결한다. 자체 frontend를 새로 만드는 초안은 등록하지 않았고, 현재 GUI 연동을 우선한다.

자료: [td-gui README](https://github.com/madic-creates/td-gui), [설치 버전 parser](https://github.com/madic-creates/td-gui/blob/v0.18.8/internal/tdbin/tdbin.go), [설치 버전 API client](https://github.com/madic-creates/td-gui/blob/v0.18.8/web/src/api/client.ts).
## Development version compatibility

`make install-worktree`, `make install-local`, and `make install` now use the
nearest reachable stable release tag as their version base, followed by
`+devel.<branch>.<commit>[.dirty]`. Branch characters are normalized to SemVer
metadata characters. With no reachable stable tag the base is `v0.0.0`, so a
consumer's minimum-version check fails conservatively. Development versions
still skip release update checks. Explicit release ldflags and module versions
from `go install module@version` are unchanged.

A plain `go build` without version ldflags still reports Go's available VCS
development identity; Go build info does not contain the ancestor release tag.
Use the installation targets for td-gui, or inject the output of
`sh scripts/dev-version.sh` via `-X main.Version=...` when building manually.

## Native sample verification (#55, 2026-10-10)

Unmodified installed td-gui v0.18.8 started on port 7778 using the default
`/opt/homebrew/bin/td`, version
`v0.66.0+devel.feat-gh-all-tasks.8c539a6.dirty`. Its owned backend used the
existing sample web session. This was a separate test instance; the user's
7777 project dashboard was left running.

| Contract | Observed result |
|---|---|
| Issue list, labels, board list, CLI TDQ query | HTTP 200 |
| Create epic and two tasks | HTTP 201, gh-109/110/111 |
| Edit task parent, acceptance, points | HTTP 200; subsequent detail matched |
| Add/delete comment | HTTP 201/200 |
| Add/delete dependency | HTTP 201/200 using returned `dep_id` for DELETE |
| Create/edit/delete board | HTTP 201/200/200, bd-gh-112 |
| Pin/unpin task | HTTP 200/200; board detail included pinned task |
| Set/clear focus | HTTP 200/200 |
| Start via REST and actual browser Start button | HTTP 200; browser displayed in_progress and td:in_progress |
| `/gui/status` CLI fallback to open | HTTP 200; fresh detail and board both showed open |
| Stale If-Match | HTTP 409 with conflict diagnostic; no write |
| Invalid priority and TDQ field | HTTP 400, field validation/invalid_query diagnostics |
| Browser Swimlanes and issue detail | Actual cards, status columns, parent and acceptance rendered |
| SSE through GUI proxy | Initial ping token followed by different refresh token at 16:58:14Z, then ping with new token |

The default issue list excludes closed tasks while a TDQ expression may include
them. The initial empty list and priority query returning the existing closed
gh-2 were consistent with those different filters, not missing data.

The SSE source observes GitHub every five minutes. GUI mutations invalidate its
local query cache, while a separate CLI write can remain invisible until the
next SSE observation. This run verified a real changed-token event, not just
an open connection. No per-GitHub-request counter was enabled for this run;
HTTP response counts here are not evidence of underlying REST call counts.

### Verified failure: review response exceeds GUI deadline

The actual browser Request review action for gh-110 (parent gh-109) displayed:
`POST /v1/issues/gh-110/review timed out after 20s — td serve is not responding`.
The backend logged HTTP 502 after 19.991 seconds with cancellation. Fresh reads
then proved **both child and parent were already in_review**. The request was
not retried. A timeout is therefore an uncertain/partially completed write,
not proof that no write happened.

`web/src/api/client.ts` uses `AbortSignal.timeout(20_000)`; the proxy reports
cancellation after that browser deadline. td's review path performs fresh
graph checks, comment/handoff writes, parent cascades and read-side availability
verification. The exact expensive stage still needs request instrumentation.
Optimize td's redundant observations without removing mutation policy or graph
conflict checks, then repeat with a fresh disposable fixture. Longer arbitrary
cascades may also require an upstream GUI timeout/uncertain-write improvement;
this evidence does not justify declaring full unchanged-GUI compatibility.

Approval/rejection/reopen, independent review attribution, precise drag/drop
gestures, owned-backend reuse/auth, native API request budgets and all cache
policies remain unverified in this run. #55 stays in progress; #48/#52 remain
required for the broader review-display/cache work. `go test ./internal/serve
-run TestGitHub` passed, including existing simulated errors and conflicts; it
does not replace native approval or SQLite regression verification.

All four created fixtures were logically deleted through the same GUI proxy;
the subsequent include_closed issue list excluded gh-109/110/111. The sample
browser tab, SSE capture and test GUI/backend were stopped. Native response
artifacts are stored in the tracking checkout under
`artifacts/td-gui-compatibility/native-sample/`.

## Review latency follow-up (2026-10-10 KST, #55 remains in progress)

The currently installed unmodified GUI reports `td-gui dev`; earlier checks
called it v0.18.8. This follow-up verifies this installed binary, not an
unverified release identity. It used a separately owned GUI on port 7778 and
`yoophi/td-sample`; existing user servers were left running.

With installed td f46d479, an ordinary child review with one epic parent
reproduced the frontend cancellation boundary using `curl --max-time 20`
against the GUI proxy. It returned curl exit 28 after 20.003 seconds; the
backend recorded HTTP 502 after 20.024 seconds, 23 API invocations and 28
observed HTTP responses. Fresh reads confirmed both gh-118 and parent gh-117
were already in_review. No review write was retried.

Parent and descendant discovery performed two consecutive full listings
before any write. They now derive their initial graphs from one fresh
listing. This is explicit per-operation reuse, not the display cache; all
subsequent graph, native-event, revision and review checks remain fresh. The
regression `TestReviewCascadeSharesInitialHierarchyObservation` failed before
the change (two initial listings) and passes afterward (one). Existing graph
conflict tests and the full ghstore race suite pass.

A second sample (parent gh-119, child gh-120) with the modified binary still
exceeded 20 seconds: curl exit 28 after 20.006 seconds, backend HTTP 502 after
20.008 seconds. At cancellation it had made 24 API invocations and observed
28 HTTP responses, including three readbacks versus two in the earlier
interrupted run. These are different interruption points, not comparable
completed-operation totals; this evidence does not establish a latency fix.
Fresh reads again confirmed both child and parent in_review.

All four fixtures were logically deleted and this owned GUI/backend stopped.
The next work is reducing duplicate post-write graph observations without
removing membership/revision conflict detection, then repeating the original
20-second reproduction. Approval/rejection and other outstanding acceptance
checks remain required before #55 closes.
