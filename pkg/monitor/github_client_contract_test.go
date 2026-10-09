package monitor

import "github.com/marcus/td/internal/ghstore"

// These assertions cover the production client, not just fixture clients.
var (
	_ GitHubMonitorReader     = (*ghstore.Client)(nil)
	_ githubDetailReader      = (*ghstore.Client)(nil)
	_ githubObservedReader    = (*ghstore.Client)(nil)
	_ githubFormWriter        = (*ghstore.Client)(nil)
	_ githubFormCreator       = (*ghstore.Client)(nil)
	_ githubTransitionWriter  = (*ghstore.Client)(nil)
	_ githubDeleteReader      = (*ghstore.Client)(nil)
	_ githubDeleteWriter      = (*ghstore.Client)(nil)
	_ githubStatsReader       = (*ghstore.Client)(nil)
	_ githubBoardClient       = (*ghstore.Client)(nil)
	_ githubBoardEditorClient = (*ghstore.Client)(nil)
	_ githubBoardMoveClient   = (*ghstore.Client)(nil)
)
