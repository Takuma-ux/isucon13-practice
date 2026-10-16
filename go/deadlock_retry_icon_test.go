package main

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Confirms withMySQLDeadlockRetry absorbs 1213 under the multi-user icon insert pattern.
func TestPostUserIconDiffUsersDeadlockRetry(t *testing.T) {
	if os.Getenv("ISUCON13_DEADLOCK_RETRY_TEST") != "1" {
		t.Skip("set ISUCON13_DEADLOCK_RETRY_TEST=1")
	}
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		os.Setenv("ISUCON13_MODERATE_CONVERGE_TEST", "1")
	}
	dbConn = testDB(t)
	ctx := context.Background()
	img := make([]byte, 512)

	run := func(retry bool, uidBase int) (ops uint64, finalErr uint64, deadlockHits uint64) {
		stop := time.Now().Add(12 * time.Second)
		var wg sync.WaitGroup
		var opsN, errN, dlN atomic.Uint64
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(wid int) {
				defer wg.Done()
				uid := int64(wid + uidBase)
				for time.Now().Before(stop) {
					opsN.Add(1)
					var err error
					if retry {
						_, err = withMySQLDeadlockRetryInt64(func() (int64, error) {
							return postUserIcon(ctx, uid, img)
						})
					} else {
						_, err = postUserIcon(ctx, uid, img)
					}
					if err != nil {
						errN.Add(1)
						if isMySQLDeadlock(err) {
							dlN.Add(1)
						}
					}
				}
			}(i)
		}
		wg.Wait()
		return opsN.Load(), errN.Load(), dlN.Load()
	}

	ops0, err0, dl0 := run(false, 40000)
	ops1, err1, _ := run(true, 50000)
	t.Logf("no retry: ops=%d final_errors=%d (deadlock_errors=%d)", ops0, err0, dl0)
	t.Logf("with retry: ops=%d final_errors=%d", ops1, err1)

	if dl0 == 0 {
		t.Skip("could not force deadlocks in this environment; retry wiring still covered by unit tests")
	}
	if err1 >= err0 {
		t.Fatalf("expected fewer final errors with retry: no_retry=%d with_retry=%d", err0, err1)
	}
}
