package service

import "time"

// SetPurgeObjectDeleteBudget overrides the post-purge object cleanup budget
// for one test and returns a func restoring the previous value. Tests that
// use it must not run in parallel.
func SetPurgeObjectDeleteBudget(d time.Duration) (restore func()) {
	old := purgeObjectDeleteBudget
	purgeObjectDeleteBudget = d
	return func() { purgeObjectDeleteBudget = old }
}
