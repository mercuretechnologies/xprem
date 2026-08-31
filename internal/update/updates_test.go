package update

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestFinalizeCheckedUpdatePublishesCheckBeforeInvalidatingLatestCache(t *testing.T) {
	uploadStarted := make(chan struct{})
	allowUpload := make(chan struct{})
	done := make(chan error, 1)
	var invalidated atomic.Bool
	prewarmed := make(chan struct{})

	go func() {
		done <- finalizeCheckedUpdate(
			func() error {
				close(uploadStarted)
				<-allowUpload
				return nil
			},
			func() { invalidated.Store(true) },
			func() { close(prewarmed) },
		)
	}()

	<-uploadStarted
	if invalidated.Load() {
		t.Fatal("latest-update cache was invalidated before .check was published")
	}
	close(allowUpload)
	if err := <-done; err != nil {
		t.Fatalf("finalizeCheckedUpdate() error = %v", err)
	}
	if !invalidated.Load() {
		t.Fatal("latest-update cache was not invalidated after .check was published")
	}
	<-prewarmed
}

func TestFinalizeCheckedUpdateUploadFailurePreservesLatestCache(t *testing.T) {
	wantErr := errors.New("upload failed")
	var invalidated atomic.Bool
	var prewarmed atomic.Bool

	err := finalizeCheckedUpdate(
		func() error { return wantErr },
		func() { invalidated.Store(true) },
		func() { prewarmed.Store(true) },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("finalizeCheckedUpdate() error = %v, want %v", err, wantErr)
	}
	if invalidated.Load() {
		t.Fatal("latest-update cache was invalidated after .check upload failed")
	}
	if prewarmed.Load() {
		t.Fatal("cache prewarm ran after .check upload failed")
	}
}
