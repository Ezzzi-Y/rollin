package mailtoken

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newTokenTestDB opens the minimal sqlite schema for the offer_token table.
func newTokenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	handle, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
CREATE TABLE offer_token (
  id INTEGER PRIMARY KEY AUTOINCREMENT, offer_id INTEGER NOT NULL,
  token_hash BLOB NOT NULL UNIQUE, created_by_task_id INTEGER, created_at DATETIME
);
`
	if err := handle.Exec(schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := handle.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return handle
}
