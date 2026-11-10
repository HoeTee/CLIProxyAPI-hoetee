package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/glmquota"
)

func TestLedgerWindowsAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.ndjson")
	ledger := NewLedger(path)
	now := time.Date(2026, 11, 8, 22, 0, 0, 0, beijing)
	ledger.now = func() time.Time { return now }
	usage := glmquota.ParseUsage([]byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":600}}}`))
	if usage == nil || usage.Cached == nil || *usage.Cached != 600 {
		t.Fatalf("parsed usage = %+v", usage)
	}
	if err := ledger.Record("alice", usage); err != nil {
		t.Fatal(err)
	}
	weekly := int64(1200)
	five := int64(1201)
	policy := config.GLMClientQuota{FiveHourTokens: &five, WeeklyTokens: &weekly}
	var rejection QuotaError
	err := ledger.Admit("alice", &policy)
	if !errors.As(err, &rejection) || rejection.Status != 429 || rejection.Message != "GLM local weekly token quota exhausted" {
		t.Fatalf("weekly decision = %v", err)
	}
	now = now.Add(2 * time.Hour)
	if errAdmit := ledger.Admit("alice", &policy); errAdmit != nil {
		t.Fatalf("week should reset at Monday midnight: %v", errAdmit)
	}
	five = 1200
	err = ledger.Admit("alice", &policy)
	if !errors.As(err, &rejection) || rejection.Message != "GLM local 5-hour token quota exhausted" {
		t.Fatalf("5-hour decision = %v", err)
	}
	if errAdmit := ledger.Admit("alice", &config.GLMClientQuota{}); errAdmit != nil {
		t.Fatalf("unlimited decision = %v", errAdmit)
	}
	if errRecord := ledger.Record("alice", nil); errRecord != nil {
		t.Fatal(errRecord)
	}
	reopened := NewLedger(path)
	snapshot, err := reopened.Snapshot("alice", now)
	if err != nil || snapshot.FiveHour.Used != 1200 || snapshot.Weekly.Used != 0 ||
		snapshot.FiveHour.Unknown != 1 || snapshot.Weekly.Unknown != 1 {
		t.Fatalf("replayed windows = %+v %+v, err = %v", snapshot.FiveHour, snapshot.Weekly, err)
	}
	hour := snapshot.Hours[21]
	if hour.CacheHit != 600 || hour.CacheMiss != 400 || hour.CacheUnknown != 0 || hour.Output != 200 {
		t.Fatalf("replayed chart hour = %+v", hour)
	}
	now = now.Add(3 * time.Hour)
	reopened.now = func() time.Time { return now }
	if errAdmit := reopened.Admit("alice", &policy); errAdmit != nil {
		t.Fatalf("5-hour window should reset at 03:00: %v", errAdmit)
	}
}
