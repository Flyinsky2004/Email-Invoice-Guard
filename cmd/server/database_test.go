package main

import (
	"bytes"
	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/storage"
	"path/filepath"
	"testing"
)

func TestDatabaseCLIOnlineSnapshotAndChecks(t *testing.T) {
	cfg := core.Config{DatabasePath: filepath.Join(t.TempDir(), "live.db"), EncryptionKey: []byte("01234567890123456789012345678901")}
	live, err := storage.Open(t.Context(), cfg.DatabasePath, cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := live.SetSetting(t.Context(), "marker", "latest committed WAL value"); err != nil {
		t.Fatal(err)
	}
	account := core.MailAccount{ID: core.NewID(), Provider: core.QQ, Address: "test@qq.com", Enabled: true}
	if err := live.SaveAccount(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	if err := live.PutCredential(t.Context(), core.Credential{AccountID: account.ID, AuthorizationCode: "fake-test-code"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	var out bytes.Buffer
	if err := databaseOperation(t.Context(), cfg, dest, true, &out); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.Open(t.Context(), dest, cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	marker, err := restored.GetSetting(t.Context(), "marker")
	if err != nil || marker != "latest committed WAL value" {
		t.Fatalf("snapshot lost committed WAL: %q %v", marker, err)
	}
	if err := databaseOperation(t.Context(), cfg, dest, false, &out); err == nil {
		t.Fatal("backup must not overwrite an existing snapshot")
	}
	bad := cfg
	bad.EncryptionKey = []byte("11234567890123456789012345678901")
	if err := databaseOperation(t.Context(), bad, "", true, &out); err == nil {
		t.Fatal("wrong key must fail credential validation")
	}
}
