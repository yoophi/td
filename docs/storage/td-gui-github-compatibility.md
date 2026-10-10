# td-gui와 GitHub 저장소 호환 조사

최신 확인: 2026-10-11 KST. 기존 설치된 td-gui의 Go module 빌드 정보는 v0.18.8/VCS 32d3050이며 이 설치본은 변경하지 않았다. 아래의 초기 조사 기록과 별도로, 호환 수정은 로컬 GUI 작업 브랜치 `fix/gh-request-timeout`에서 검증 중이다.

## 현재 결론

기동과 확인한 조회 기능에는 기존 td-gui를 사용할 수 있다. td 개발 빌드의 version 형식을 실제 기반 SemVer + development metadata로 바꾸는 #54는 완료됐다. 그러나 GitHub 저장소의 전체 GUI 사용에는 GUI 쪽 수정이 필요하다는 실제 실패를 확인했다. #59–#62 및 #71 수정본의 검증을 기존 설치본의 지원 범위로 간주하면 안 된다.

로컬 GUI 수정본은 `f5b2aba`다. upstream 반영 또는 별도 fork의 배포 경로 확정은 #53에 남아 있다. 기존 설치본을 교체하거나 수정본을 공개 배포했다는 뜻은 아니다. #55의 실제 마우스 드래그와 최종 계약 감사도 아직 완료되지 않았다.

`td serve`는 API 서버이고 `td-gui`가 별도 포트에서 웹 화면을 제공한다. 따라서 td serve의 `/`에서 unsupported_operation이 나와도 GUI 연동을 판단할 수 없다. GUI가 출력하는 URL을 사용한다. `website/`는 문서 사이트다.

### 현재 확인한 범위

| 범위 | 최신 근거/제약 |
|---|---|
| 생성·제목/설명 편집·댓글·의존성·부모·focus | sample의 실제 브라우저 변경과 GitHub 원격 결과 확인 |
| Start/review/자체 리뷰 승인/reopen | 실제 브라우저 및 실제 web session 귀속 확인; 독립 리뷰로 표시하지 않음 |
| 보드 생성·편집·위치 변경 | 생성/편집과 Move up/down/Unpin 버튼을 실제 브라우저에서 확인; 위치 순서는 native GitHub metadata와 일치 |
| 실제 카드 드래그 | 미검증. 자동화에서 trusted dragstart는 발생하지만 drop이 전달되지 않음. 이것만으로 GUI 결함이나 지원 불가를 판정하지 않음 |
| 드롭 이후의 상태 전환 창 | 실제 SPA에 합성 drop을 전달하면 창이 열리고 native Start 클릭으로 카드 이동; 물리 드래그의 대체 완료 증거는 아님 |
| CLI 상태 복귀 | 편집 화면의 open 복귀와 실제 unstart 기록 확인; REST web session과 CLI subprocess session은 별개 |
| SSE 외부 변경 | 실제 CLI 변경 후 refresh와 브라우저 갱신 확인. 기본 GitHub 관측 간격은 5분이며 즉시 반영을 보장하지 않음 |
| 오류 표시 | 조회 실패/부분 목록, 잘못된 TDQ, rate-limit 원문 확인. #71은 상태 CLI 오류의 모든 줄과 제공된 구조화 코드를 보존; 오류 주입 브라우저 검증은 실제 GitHub 제한 발생과 구분 |
| SQLite 회귀 | 실제 td를 사용하는 GUI 계약 테스트 통과. SQLite 상태 거부가 --json에서 무출력일 수 있어 상태 CLI의 일반 호출을 유지 |

GUI 수정은 고정 20초 쓰기 취소와 불확실한 결과 안내(#59), 4xx 조회 반복 및 삭제 후 화면 이동(#60), 조회 실패를 빈 목록으로 표시하는 문제(#61), TDQ 오류를 얻기 위한 중복 CLI 실행(#62), 상태 CLI 오류 원문 누락(#71)을 다룬다. 실패한 쓰기를 자동 재실행하지 않는다. 현재 GUI의 선택적 If-Match 한계는 아래 설명대로 유지된다.

최근 sample 검증은 td `d1b5bbf`와 별도 GUI/server에서 진행했다. 오래된 monitor·serve 프로세스는 설치 바이너리를 갱신해도 바뀌지 않으므로 새 journal schema를 쓰기 전에 재시작해야 한다. 기록은 추적 checkout의 `artifacts/td-gui-compatibility/kanban-final/` 및 #55/#71에 남긴다.

이하 절은 당시 버전별 조사 기록이다. 각 절의 미검증 항목은 위 최신 확인 범위와 함께 해석한다.

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

### Shared post-review hierarchy verification

The final fresh descendant check now returns its complete listing. The
immediately adjacent parent check validates ancestor membership, direct
children and revisions from that same observation; no remote write intervenes.
Each subsequent parent write still has its own fresh before/after graph checks.
The two-parent regression failed with seven listings before this change and
passes with six afterward (initial + shared post-review + two per parent).
The full ghstore race suite and Mac/Linux lint pass, including existing
reparenting, concurrent edits and partial-write conflict cases.

The original 20-second proxy reproduction now completed for new sample
gh-122 with one epic parent gh-121: HTTP 200 in 19.879 seconds, both child and
parent in_review, available transitions approve/reject. This completed request
made 27 API invocations and observed 31 HTTP responses (preflight 1, issue
listing 4 invocations/8 pages, issue detail 7, issue comments 1, label setup 1,
review validation 7, write readback 3, writes 3). Earlier timeout measurements
were incomplete totals. The measured success has only about 0.12 seconds of
margin; it does not guarantee ordinary network variation or larger cascades
will fit the GUI timeout.

On the same owned GUI proxy, an honest self-review record-only request
`POST /v1/issues/gh-122/reviews` returned 201 in 7.439 seconds and preserved
the real web actor with self_review=true. Subsequent reject returned 200 in
6.521 seconds, reset the child to open and cleared active review attribution.
These are HTTP contract checks, not new browser click evidence or independent
human review. The upstream source checkout a9aefa5 contains explicit mutually
exclusive attributed/self-review UI choices; no fabricated reviewer was used.
Both fixtures were logically deleted and the owned GUI/backend stopped.
#55 remains in progress for the outstanding complete acceptance audit.

### Native browser workflow and backend discovery audit

With installed td edac4ab and the unchanged GUI, Chrome native UI verification
on a separately owned sample GUI (7778) observed gh-123 Start→in_progress,
Request review→in_review, Approve→closed and Reopen→open. Approval explicitly
selected “I reviewed my own work” with an honest reason. The resulting screen
displayed self-reviewed and the actual web actor, not a fabricated independent
reviewer; reopening cleared the active reviewer fields. Backend responses
were all 200: start4.929s, review16.570s, approve15.052s, reopen5.540s.

`go version -m ~/.local/bin/td-gui` establishes module v0.18.8 and unmodified
VCS32d3050cdb3ae415d717bc1e93928a683886e123. Its `-version` output remains dev;
this is a build banner limitation, not proof of a different module release.

The native UI rendered sample gh-123/gh-124 in All Issues swimlanes. Three
coordinate drags of gh-124 toward In progress produced no transition panel,
no server write and no status change. This is an unverified real drag/drop
interaction; it does not establish an API bug or a requirement to fork GUI.
Backlog position HTTP contracts were previously checked, but a successful
real drag/drop still needs evidence before the complete #55 audit can pass.

Starting a second unchanged GUI against the same sample while the first owned
a bearer-protected backend exited1 with the explicit unknown-token error. It
did not replace or stop the existing backend; the original GUI health remained
200. This is the upstream manager's intentional authentication boundary, not
a successful authenticated reuse. Unauthenticated existing-backend reuse is
still a separate acceptance check.

The full internal/serve race suite (GitHub and SQLite HTTP tests) passed.
Owned browser tab and GUI/backend were stopped, both fixtures logically
deleted, and the user's original7777 tab restored. Existing user servers were
left intact. Evidence is recorded under the tracking checkout's
artifacts/td-gui-compatibility/browser-workflow. #55 remains in_progress;
real drag/drop, unauthenticated reuse and the remaining acceptance audit
must not be inferred from these narrower successful workflow checks.

### Existing unauthenticated backend reuse (verified)

Started a separately owned installed td server on loopback with no bearer
token against td-sample, then launched the unchanged GUI on7778. The sample
discovery file recorded port53743, PID19932 and instance srv_efc6d1 before
GUI startup. All discovery fields were identical afterward, and proxy health
returned200 for gh-issue. After terminating only this GUI, the same server
PID and discovery fields remained, and direct backend health still returned
200. The separately owned server was then stopped explicitly. This proves
reuse and lifecycle ownership without removing authentication from any
existing user server. Evidence: artifacts/td-gui-compatibility/backend-reuse.

A further isolated gh-125 drag check tested the coordinate-scale hypothesis.
Half-sized coordinates selected the board title instead of the card, so that
hypothesis did not explain the earlier failure. Original coordinates at the
card edge also produced no transition panel or server write. The fixture was
logically deleted, its GUI stopped and its browser tab closed; the original
user7777 tab remained selected. Real drag/drop remains unverified.
Unauthenticated backend reuse is now complete; the preceding outstanding
list is historical and must not be read as its current status.
