package inspect

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInstallFailureFinishesRecoveryBeforeRemovingStage(t *testing.T) {
	for _, tc := range []struct {
		name                string
		phase               installPhase
		interruptedSnapshot bool
		steps               []string
	}{
		{"snapshot interrupted", quiescingUpgrade, true, []string{"cleanup-install", "resume-upgrade", "cleanup-install"}},
		{"acceptance failed", publishingUpgrade, false, []string{"restore-gateway-network", "rollback-upgrade", "cleanup-install"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := t.TempDir()
			trace := filepath.Join(stage, "trace")
			t.Setenv("TGCLOUD_INSTALL_TEST_TRACE", trace)
			worker := filepath.Join(stage, "worker")
			if err := os.WriteFile(worker, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$TGCLOUD_INSTALL_TEST_TRACE\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if tc.phase == publishingUpgrade {
				if err := os.WriteFile(filepath.Join(stage, gatewayNetworkJournal), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			tx := installTransaction{temps: InstallTemps{Stage: stage}, phase: tc.phase, executable: worker, output: io.Discard,
				snapshotInProgress: tc.interruptedSnapshot}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.interruptedSnapshot {
				cancel()
			}
			if err := tx.finish(ctx, errors.New("fixture failure")); err == nil {
				t.Fatal("failure was reported as success after recovery")
			}
			calls, err := os.ReadFile(trace)
			if err != nil || !slices.Equal(strings.Fields(string(calls)), tc.steps) {
				t.Fatalf("recovery order: %s, want %v; %v", calls, tc.steps, err)
			}
		})
	}
}
