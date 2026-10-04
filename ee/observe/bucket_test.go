// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBucketIgnoresTheSnapOverhang(t *testing.T) {
	require.Equal(t, 15*time.Minute, Bucket(24*time.Hour))
	require.Equal(t, 15*time.Minute, Bucket(24*time.Hour+4*time.Minute))
	require.Equal(t, 5*time.Minute, Bucket(6*time.Hour+4*time.Minute))
	require.Equal(t, time.Hour, Bucket(7*24*time.Hour+59*time.Minute))
	require.Equal(t, 5*time.Minute, Bucket(time.Hour))
}
