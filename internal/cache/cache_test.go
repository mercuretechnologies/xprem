package cache

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseSentinelAddrsTrimsWhitespaceAndDropsEmptyEntries(t *testing.T) {
	got := parseSentinelAddrs(" sentinel-0:26379, sentinel-1:26379,,\t sentinel-2:26379 , ")
	want := []string{"sentinel-0:26379", "sentinel-1:26379", "sentinel-2:26379"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSentinelAddrs() = %#v, want %#v", got, want)
	}
}

func TestParseSentinelAddrsReturnsEmptyForBlankInput(t *testing.T) {
	got := parseSentinelAddrs(" , \t,")

	if len(got) != 0 {
		t.Fatalf("parseSentinelAddrs() = %#v, want empty", got)
	}
}

// Two Gets racing on an expired key used to delete under the read lock: a
// concurrent map read+write, which the runtime kills unrecoverably. Run with
// -race; the old code fails the detector, the write-lock upgrade is clean.
func TestLocalCacheConcurrentExpiredGets(t *testing.T) {
	c := NewLocalCache()
	zero := 0
	require.NoError(t, c.Set("expired-key", "value", &zero))
	time.Sleep(5 * time.Millisecond)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Get("expired-key")
		}()
	}
	wg.Wait()
	require.Equal(t, "", c.Get("expired-key"))
}

func TestKeyEscapesSeparatorSegments(t *testing.T) {
	require.Equal(t, "lastUpdate:1.0.0:app:main:52.0.0:ios", Key("lastUpdate", "1.0.0", "app", "main", "52.0.0", "ios"))

	// A ':' inside a segment must not collide two different segment tuples
	// onto one key: (branch "x", rt "1:evil") vs (branch "x:1", rt "evil").
	require.NotEqual(t, Key("k", "x", "1:evil"), Key("k", "x:1", "evil"))
	// Nor may a literal "%3A" collide with an escaped ':'.
	require.NotEqual(t, Key("k", "x%3A1"), Key("k", "x:1"))
	require.Equal(t, "k:exposdk%3A52.0.0", Key("k", "exposdk:52.0.0"))
}
