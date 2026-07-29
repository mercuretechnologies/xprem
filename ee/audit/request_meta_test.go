// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"testing"
	"xprem/internal/auditlog"

	"github.com/stretchr/testify/require"
)

func TestRecordFillsRequestMetaFromContext(t *testing.T) {
	repo := &fakeAuditRepo{}
	service := enabledService(repo)
	ctx := auditlog.WithRequestMeta(context.Background(),
		auditlog.RequestMeta{IP: "198.51.100.3", UserAgent: "cli/2.0"})

	service.Record(ctx, Event{Action: auditlog.ActionUserLogin})
	// An event that already carries its own network facts keeps them.
	service.Record(ctx, Event{Action: auditlog.ActionUserLogin, IP: "192.0.2.1", UserAgent: "custom"})

	require.Len(t, repo.inserted, 2)
	require.Equal(t, "198.51.100.3", repo.inserted[0].IP)
	require.Equal(t, "cli/2.0", repo.inserted[0].UserAgent)
	require.Equal(t, "192.0.2.1", repo.inserted[1].IP)
	require.Equal(t, "custom", repo.inserted[1].UserAgent)
}
