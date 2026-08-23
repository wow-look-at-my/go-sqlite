// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestBackupCommitClosesConnOnError verifies that Commit() closes the
// destination connection when sqlite3_backup_finish returns an error.
// Before the fix, the error branch returned (nil, err) without closing
// dstConn, leaking the file descriptor, TLS, and memory.
//
// Strategy: create a backup whose destination is a file-based database,
// then make the destination file read-only so that backup_step fails
// with an I/O or read-only error. Commit() must still close the
// destination connection.
func TestBackupCommitClosesConnOnError(t *testing.T) {
	// Create source in-memory database with some data.
	srcConn, err := newConn(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer srcConn.Close()

	// Populate source so there is at least one page to copy.
	if err := execConn(srcConn, "CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	if err := execConn(srcConn, "INSERT INTO t VALUES('hello')"); err != nil {
		t.Fatal(err)
	}

	// Create a temp directory for the destination database.
	tmpDir, err := os.MkdirTemp("", "backup_commit_test_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dstPath := filepath.Join(tmpDir, "dst.db")

	// Create the backup object (this opens the destination connection).
	bck, err := srcConn.NewBackup(dstPath)
	if err != nil {
		t.Fatal(err)
	}

	// Make the destination directory read-only so that writes to the
	// destination database fail. This causes backup_step to return an
	// error (SQLITE_READONLY or SQLITE_IOERR), which backup_finish
	// propagates.
	if err := os.Chmod(dstPath, 0o444); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	if err := os.Chmod(tmpDir, 0o555); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	// Restore write permission on cleanup so RemoveAll succeeds.
	defer os.Chmod(tmpDir, 0o755)

	// Step should fail because the destination is read-only.
	_, stepErr := bck.Step(-1)

	// Whether or not Step reported the error, Commit must propagate the
	// error from backup_finish and close the destination connection.
	conn, commitErr := bck.Commit()

	if stepErr == nil && commitErr == nil {
		// If neither errored, the test setup didn't achieve its goal.
		// Close the returned connection to avoid a leak in this edge case
		// and skip.
		if conn != nil {
			conn.Close()
		}
		t.Log("chmod did not restrict writes on this platform")
		t.Skip("could not provoke backup error on this platform; skipping")
	}

	if conn != nil {
		t.Fatal("Commit() must return nil conn on error")
	}

	// The critical assertion: the destination connection must be closed.
	// After Close(), db is set to 0.
	if bck.dstConn.db != 0 {
		t.Fatal("Commit() did not close the destination connection on error")
	}
}

// TestBackupProgress verifies that Remaining and PageCount expose the
// underlying sqlite3_backup_remaining / sqlite3_backup_pagecount values
// across the three phases of a backup: before any Step has run, after a
// partial Step, and after the backup completes (SQLITE_DONE).
//
// The contract under test:
//   - Before any Step, both methods report 0.
//   - After a partial Step, PageCount equals the source database page count
//     and Remaining equals PageCount minus pages already copied.
//   - After the backup completes, Remaining is 0 and PageCount stays at the
//     source page count.
func TestBackupProgress(t *testing.T) {
	srcConn, err := newConn(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer srcConn.Close()

	if err := execConn(srcConn, "CREATE TABLE t(x INTEGER, y TEXT)"); err != nil {
		t.Fatal(err)
	}
	// Insert enough rows that the source database spans multiple pages so
	// the partial-Step phase is observable.
	for i := 0; i < 200; i++ {
		if err := execConn(srcConn, "INSERT INTO t(x, y) VALUES(1, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"); err != nil {
			t.Fatal(err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "backup_progress_test_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dstPath := filepath.Join(tmpDir, "dst.db")

	bck, err := srcConn.NewBackup(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer bck.Finish()

	// Phase 1: before any Step, both counters are 0 per the SQLite docs.
	if got := bck.Remaining(); got != 0 {
		t.Errorf("Remaining before Step: got %d, want 0", got)
	}
	if got := bck.PageCount(); got != 0 {
		t.Errorf("PageCount before Step: got %d, want 0", got)
	}

	// Phase 2: copy one page so PageCount becomes known and Remaining is
	// strictly positive (the source spans many pages).
	more, err := bck.Step(1)
	if err != nil {
		t.Fatalf("first Step: %v", err)
	}
	if !more {
		t.Fatal("first Step returned DONE on a multi-page source; test setup is too small")
	}

	pages := bck.PageCount()
	if pages <= 1 {
		t.Fatalf("PageCount after first Step: got %d, want > 1", pages)
	}
	remaining := bck.Remaining()
	if remaining <= 0 {
		t.Errorf("Remaining after copying 1 page: got %d, want > 0", remaining)
	}
	if remaining != pages-1 {
		t.Errorf("Remaining after copying 1 page: got %d, want %d (PageCount - 1)", remaining, pages-1)
	}

	// Phase 3: finish the copy and confirm Remaining drops to 0 while
	// PageCount keeps reporting the source page count.
	more, err = bck.Step(-1)
	if err != nil {
		t.Fatalf("final Step: %v", err)
	}
	if more {
		t.Fatal("final Step with n=-1 returned more=true; expected DONE")
	}
	if got := bck.Remaining(); got != 0 {
		t.Errorf("Remaining after DONE: got %d, want 0", got)
	}
	if got := bck.PageCount(); got != pages {
		t.Errorf("PageCount after DONE: got %d, want %d (unchanged from mid-backup)", got, pages)
	}
}

// execConn is a test helper that executes a SQL statement on a raw conn.
func execConn(c *conn, sql string) error {
	s, err := c.prepare(context.Background(), sql)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = s.(*stmt).ExecContext(context.Background(), nil)
	return err
}
