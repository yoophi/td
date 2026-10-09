# GitHub 조회 N+1 제거 계획

2026-10-10 조사. 구현 전 계획이며 완료된 최적화로 간주하지 않는다.

## 관찰 결과

- 우선순위 P0–P4는 이미 issue 목록의 body metadata에서 읽는다. 라벨로 옮겨도 댓글 이력 조회의 N+1은 없어지지 않는다.
- `internal/ghstore/change_token.go`는 모든 issue마다 댓글 목록을 조회한다. 이슈 목록 페이지 I와 이슈별 댓글 페이지 Cᵢ에 대해 I + ΣCᵢ가 필요하며, 댓글이 없어도 이슈마다 한 요청이 발생한다.
- `pkg/monitor/github_data.go`, `internal/serve/github_monitor.go`, `pkg/monitor/github_summary.go`, `internal/ghstore/stats.go`는 각 task의 activity 조회로 상세 issue와 댓글을 다시 읽는다. JSON export는 상세 재조회가 제거되었지만 댓글 조회는 여전히 이슈별이다.
- `ObserveMonitorReview`는 in_review 항목마다 native state events와 handoff activity 및 issue revision을 재조회한다. 댓글 일괄 조회만 적용해도 이 부분은 추가 N+1로 남는다.
- 2026-10-10 실제 `yoophi/td`에서 저장소 전체 댓글 99개(33개 issue/PR 번호)를 **1페이지**로 읽었다. 계획 이슈 등록 전 수치이며 댓글 수 증가 시 페이지 수가 늘어난다. 기존 44개 issue 변경 감지는 45회, preflight 포함 46회였다.

## 선택한 조회 방식

첫 구현은 기존 `gh api` REST transport를 유지한다.

1. `GET /repos/{owner}/{repo}/issues?state=all&per_page=100` 전체 페이지.
2. 댓글이 필요한 경우 `GET /repos/{owner}/{repo}/issues/comments?per_page=100` 전체 페이지.
3. `issue_url`로 댓글을 조인하고 같은 snapshot에 원본/파싱된 issue와 activity를 보관한다.
4. 해당 snapshot을 변경 감지와 화면·통계·export에서 재사용한다. 댓글이 필요 없는 기본 목록은 issue 페이지만 읽는다.

전체 수집 비용은 **I + C** 페이지 요청이며 preflight는 별도다. issue/comment가 각각 한 페이지라면 preflight 포함 약 3회다. 모든 저장소에서 3회로 고정되는 것은 아니다. PR 댓글 필터링, auxiliary entity, closed/논리 삭제, 중복·불완전 페이지 및 목록/댓글 사이의 변경을 명시적으로 처리한다. snapshot은 원자적인 원격 revision이 아니다.

GitHub의 [저장소 전체 issue 댓글 API](https://docs.github.com/en/rest/issues/comments#list-issue-comments-for-a-repository)는 PR의 일반 댓글도 포함하며, `issue_url`, `since`, 최대 100개 페이지를 제공한다. PR 코드 리뷰 댓글은 다른 API다.

GraphQL은 별도 quota를 사용하지만 필수 변경으로 잡지 않는다. 중첩 comments 연결은 각각 cursor를 끝까지 처리해야 하며 일부 결과/오류를 성공으로 간주할 수 없다. [공식 query 제한](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api)에 맞춘 bounded batch 대안은 REST bulk의 실제 비용이 부족한 경우 검토한다. GraphQL 전환 자체가 N+1 제거의 완료 기준은 아니다.

## 작업 및 순서

| 순서 | 우선순위 | 작업 | 의존성 |
|---|---|---|---|
| 1 | P1 | [저장소 댓글 일괄 조회 + 공통 snapshot](https://github.com/yoophi/td/issues/46) | 없음 |
| 2 | P1 | [serve/TUI/통계/export 소비 경로 전환](https://github.com/yoophi/td/issues/47) | 1 |
| 3 | P1 | [목록 리뷰 표시와 실제 승인 검증 분리](https://github.com/yoophi/td/issues/48) | 1, 2 |
| 4 | P1 | [route별 요청 비용 회귀 테스트/계측](https://github.com/yoophi/td/issues/51) | 기준선부터 작성, 1–3 적용 후 완료 |
| 5 | P2 | [우선순위 라벨 및 기존 metadata 호환 마이그레이션](https://github.com/yoophi/td/issues/49) | 별도 기능, bulk 완료의 선행 조건 아님 |
| 5a | P1 | [macOS 표준 위치의 디스크 snapshot 캐시](https://github.com/yoophi/td/issues/52) | 1, 소비 경로 연동은 2 |
| 6 | P2 | [중복 polling 합치기·증분 조회·전체 대조](https://github.com/yoophi/td/issues/50) | 1–4 및 디스크 캐시 |

serve 우선순위에 맞춰 공통 수집 후 serve 경로부터 전환하고 TUI 및 나머지 소비자를 같은 수집기로 연결한다. 먼저 요청 수 기준선을 확보한다.

## 라벨 정책

`td:priority:P0`–`td:priority:P4`로 GitHub UI 표시와 서버 측 필터를 지원한다. 현재 상태에 적용한 원칙과 호환되도록 첫 단계는 **metadata 기준 + 라벨 표시**다. metadata를 삭제하고 라벨을 유일한 기준으로 만드는 변경은 별도 schema/version·구버전 writer·export/import·외부 라벨 편집 충돌 정책을 갖춘 후 진행한다. 기존 라벨 없는 task를 필터에서 누락하지 않는다. migration은 dry-run과 revision 충돌 검사, 부분 완료 안내를 갖춘다.

상태는 이미 합의한 metadata/native 기준을 유지한다. type 라벨은 실제 필터 용도를 확인하여 추가하고, points·session·dependency 등 구조화된 데이터를 일괄 라벨화하지 않는다.

## 정합성과 안전 검증

- 목록 리뷰 표시는 snapshot에서 판단 가능한 사실만 표시한다. native 이벤트 이력이 미검증이면 확인 필요 상태를 노출한다. 상세 선택 및 실제 승인/닫기 시 해당 issue의 최신 원격 상태와 handoff, close/reopen 이력, 참여 세션 정책을 검증한다.
- 읽기 최적화 때문에 실제 mutation의 최신 충돌 검사를 제거하지 않는다. 개별 쓰기의 필수 readback은 목록 N+1 제거 예산과 구분한다.
- `since` 증분은 삭제 tombstone을 제공하지 않으므로 정기 전체 대조가 필요하다. 마지막 성공 관찰 시각과 삭제 반영 지연을 문서화하며 모든 페이지 검증 전 cursor를 전진시키지 않는다.
- 프로세스 내부 조회 합치기/TTL로 같은 sweep을 재사용한다. repo/auth가 다른 캐시를 공유하지 않는다. 별도 프로세스 전체의 계정 quota를 보장하지 않는다.
- 기존 5분 최소 polling과 reset까지 cooldown을 유지하고 원문 에러를 보존한다. 자동 쓰기 재시도를 도입하지 않는다.

## 완료 기준

ChangeToken, serve, TUI, stats 및 전체 JSON export에서 전체 목록을 읽기 위한 이슈별 detail/comments/events HTTP 요청은 0이다. 페이지 수 I+C를 비용 기준으로 검증한다. 댓글 없는 목록은 C=0이다. 0/1/45/100초과 issue, 100초과 댓글, 댓글 편집/삭제, PR/auxiliary, corrupt/partial pagination 및 rate-limit을 검증한다. export는 전체 이력을 유지하고 조회 실패 시 부분 백업 파일을 공개하지 않는다. 실제 승인/닫기 검증은 계속 최신 원격 상태를 확인한다.

각 작업의 상세 설명과 검증 기준은 연결된 GitHub 이슈에 기록한다.

## macOS 데이터 규칙을 적용한 캐시 저장 설계

사용자가 제공한 클립보드 규칙은 재생성 가능한 데이터는 Caches, 보존할 데이터는 Application Support에 저장한다는 원칙이다. [Apple 파일 시스템 가이드](https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/FileSystemProgrammingGuide/FileSystemOverview/FileSystemOverview.html)의 Library 디렉터리 규칙과 일치한다. td는 Go CLI이므로 Swift FileManager 대신 [os.UserCacheDir](https://pkg.go.dev/os#UserCacheDir)를 사용한다. macOS에서는 사용자 Library/Caches, Linux에서는 XDG 캐시 경로가 적용된다. 향후 sandbox GUI에서는 컨테이너의 표준 디렉터리 resolver를 별도로 사용한다.

### 위치와 형식

`filepath.Join(os.UserCacheDir(), "td", "gh-issue", "v1", contextKey, "snapshot.json")`

macOS 예시: `~/Library/Caches/td/gh-issue/v1/<context-key>/snapshot.json`.

CLI에는 Bundle ID가 없어 `td`를 애플리케이션 namespace로 사용한다. 첫 구현은 task SQLite DB와 분리된 JSON 파일 하나를 snapshot 단위로 저장한다. 조회용 디스크 인덱스가 실제로 필요한 대규모 저장소가 확인되면 압축 또는 별도 cache DB를 후속 검토한다.

| 항목 | 캐시 데이터 |
|---|---|
| 격리 키 | API host, 정규화된 repository, 인증 사용자/인증 컨텍스트, 조회 범위, schema version |
| 헤더 | version, repo/account identity, scope, collected_at, complete, change token |
| 내용 | bulk 수집한 issue와 comment 원본 및 안정적인 ID 매핑; 읽기 시 현행 decoder로 검증 |
| 증분 상태 | 성공한 수집의 cursor, 마지막 전체 대조 시각; 증분 구현 단계에서 추가 |
| 저장하지 않는 것 | token/인증 헤더, 설정/세션의 유일한 사본, pending write/import 복구 기록 |

`issues-only` 캐시를 `full-history`로 오인하지 않는다. 인증 컨텍스트를 구분할 수 없으면 디스크 캐시 공유를 끈다. 인증 변경/로그아웃/권한 거부 시 관련 캐시를 무효화하고 권한 오류를 숨기지 않는다. 토큰을 저장하거나 로그로 출력하지 않는다.

### 저장과 동시성

디렉터리 0700, 파일 0600. 전체 페이지와 metadata 검증 후 같은 디렉터리의 임시 파일에 쓰고 sync/close/rename하여 snapshot을 공개한다. 실패한 pagination/파싱 결과는 cache 성공으로 저장하지 않는다. 부분 파일, 잘못된 scope/version, 손상 데이터는 cache miss다.

프로세스 내 요청 합치기를 먼저 적용한다. 프로세스 간에는 짧은 publication lock과 generation 비교를 사용하여 오래된 수집이 새 결과나 mutation 후 invalidation을 덮어쓰지 못하게 한다. network 호출 중 publication lock을 유지하지 않는다. 최초 버전은 별도 프로세스의 동시에 시작한 원격 요청까지 모두 합치지는 않는다. 이 한계를 실제 요청 계측에 포함한다.

### 갱신, 실패 및 청소

- 기본 TTL 제안은 5분이다. 초기 버전은 TTL 만료 때 bulk 전체 수집을 수행하여 댓글 삭제 감지도 유지한다. 증분은 후속 작업에서 별도 전체 대조 주기와 함께 적용한다.
- 누락/손상/삭제/캐시 경로 접근 실패는 경고가 필요한 경우 알리고 직접 원격에서 조회한다. 캐시가 없어도 정상 온라인 동작한다.
- `gh` 부재, GitHub origin 부재, 인증·권한 오류는 캐시로 감추지 않는다. cache hit는 issue/comment API 0회를 목표로 하고 실제 접근 preflight 비용은 별도다.
- rate-limit/연결 실패 시 화면은 last-known 결과를 유지할 수 있으나 관찰 시각, stale 상태, 원문 오류/reset 안내를 명시한다. 일반 CLI/export는 명시적 stale 허용 없이 오래된 결과를 최신 성공으로 내보내지 않는다.
- 쓰기 성공 또는 결과 불확실 시 관련 generation을 무효화한다. 실제 승인/닫기/update의 최종 원격 검증은 캐시를 우회한다. 조회 캐시는 쓰기 복구 journal을 대신하지 않는다.
- 제안 UI: `td cache status`, `td cache clear [--all]`, 조회 명령 `--refresh`. clear는 재생성 가능한 조회 데이터만 지운다. `--refresh`도 rate-limit cooldown을 무시하지 않는다.
- 용량 정책 초안: 총 256MiB 및 7일 미사용 정리. 상한을 넘는 snapshot은 디스크 저장을 생략하며 온라인 기능은 유지한다. 활성 reader와 concurrent publication을 보호하고 현재 프로세스의 고아 임시 파일도 정리한다.

보존이 필요한 설정/세션/pending write 기록은 캐시로 이동하지 않는다. 새로운 durable 사용자 데이터는 Go `os.UserConfigDir()` 기반 Application Support 또는 기존 명시적인 프로젝트 상태에 저장한다. 사용자 backup/export는 사용자가 지정한 경로를 사용한다.

### 검증

표준 경로 resolver, 계정/repo/scope 격리, TTL hit/miss, 권한 없음/삭제/손상/schema 변경, 중간 프로세스 종료, 동시 publication 및 mutation invalidation, 전체 페이지 실패, rate-limit와 stale 표시, 청소 후 원격 복구를 검증한다. 캐시 hit의 API 수와 mutation의 fresh 검증 요청을 구분한다.
