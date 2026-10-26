package main

import (
	"os"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

var (
	sharedTestDB     *sqlx.DB
	sharedTestDBOnce sync.Once
	sharedTestDBErr  error
)

// sharedIntegrationDB returns one *sqlx.DB for the whole test process (sets global dbConn).
// Callers must not Close it; TestMain closes it after m.Run().
func sharedIntegrationDB() (*sqlx.DB, error) {
	sharedTestDBOnce.Do(func() {
		sharedTestDB, sharedTestDBErr = connectDB(nil)
		if sharedTestDBErr == nil {
			dbConn = sharedTestDB
		}
	})
	return sharedTestDB, sharedTestDBErr
}

func requireSharedIntegrationDB(t *testing.T) *sqlx.DB {
	db, err := sharedIntegrationDB()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return db
}

func sharedIntegrationDBOrSkip(t *testing.T) *sqlx.DB {
	db, err := sharedIntegrationDB()
	if err != nil {
		t.Skipf("db unavailable: %v", err)
	}
	return db
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedTestDB != nil {
		_ = sharedTestDB.Close()
		sharedTestDB = nil
		dbConn = nil
	}
	os.Exit(code)
}
