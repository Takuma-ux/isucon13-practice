package main

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
)

// readQueryer is satisfied by *sqlx.DB and *sqlx.Tx for read-only SELECT paths.
type readQueryer interface {
	Rebind(string) string
	SelectContext(context.Context, interface{}, string, ...interface{}) error
	GetContext(context.Context, interface{}, string, ...interface{}) error
}

const maxMySQLDeadlockRetries = 3

func isMySQLDeadlock(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1213
	}
	return false
}

func withMySQLDeadlockRetry(fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxMySQLDeadlockRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if isMySQLDeadlock(lastErr) {
			continue
		}
		return lastErr
	}
	return lastErr
}

func withMySQLDeadlockRetryInt64(fn func() (int64, error)) (int64, error) {
	var lastErr error
	var v int64
	for attempt := 0; attempt < maxMySQLDeadlockRetries; attempt++ {
		v, lastErr = fn()
		if lastErr == nil {
			return v, nil
		}
		if isMySQLDeadlock(lastErr) {
			continue
		}
		return 0, lastErr
	}
	return 0, lastErr
}
