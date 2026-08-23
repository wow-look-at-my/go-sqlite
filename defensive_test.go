// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "github.com/wow-look-at-my/go-sqlite"

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlite3 "github.com/wow-look-at-my/go-sqlite/lib"
)

func TestDefensiveConfigCallVaList(t *testing.T) {
	c, err := newConn(":memory:")
	require.Nil(t, err)

	defer c.Close()

	rc := c.dbConfigBool(sqlite3.SQLITE_DBCONFIG_DEFENSIVE, true)
	require.Equal(t, sqlite3.SQLITE_OK, rc)

	rc := c.dbConfigBool(sqlite3.SQLITE_DBCONFIG_DEFENSIVE, false)
	require.Equal(t, sqlite3.SQLITE_OK, rc)

}

func TestDefensiveDSNValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"invalid", "?_defensive=not-a-bool&_pragma=schema_version(41)", "invalid _defensive"},
		{"empty", "?_defensive=&_pragma=schema_version(41)", "invalid _defensive"},
		{"duplicate_same", "?_defensive=1&_defensive=1&_pragma=schema_version(41)", "exactly once"},
		{"duplicate_conflicting", "?_defensive=1&_defensive=0&_pragma=schema_version(41)", "exactly once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "invalid.db")
			db, err := sql.Open("sqlite", dbPath+tc.query)
			require.Nil(t, err)

			defer db.Close()
			err = db.Ping()
			require.False(t, err == nil || !strings.Contains(err.Error(), tc.want))

			_, statErr := os.Stat(dbPath)
			require.True(t, os.IsNotExist(statErr))

		})
	}
}

func TestDefensiveModeBlocksDangerousOperations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "defensive.db")
	db, err := sql.Open("sqlite", dbPath+"?_defensive=1")
	require.Nil(t, err)

	defer db.Close()

	_, err = db.Exec("CREATE TABLE protected(id INTEGER PRIMARY KEY)")
	require.Nil(t, err)

	var journalMode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode=OFF").Scan(&journalMode))

	require.False(t, strings.EqualFold(journalMode, "off"))

	var before, after int64
	require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&before))

	_, err = db.Exec(fmt.Sprintf("PRAGMA schema_version=%d", before+41))
	require.Nil(t, err)

	require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&after))

	require.Equal(t, before, after)

	_, err = db.Exec("PRAGMA writable_schema=ON")
	require.Nil(t, err)

	_, err = db.Exec("DELETE FROM sqlite_schema WHERE name='protected'")
	require.NotNil(t, err)

	var protectedCount int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='protected'").Scan(&protectedCount))

	require.Equal(t, 1, protectedCount)

}

func TestDefensiveModeAppliesBeforeUserPragma(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ordering.db")
	db, err := sql.Open("sqlite", dbPath+"?_defensive=1&_pragma=journal_mode(OFF)")
	require.Nil(t, err)

	defer db.Close()

	var journalMode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journalMode))

	require.False(t, strings.EqualFold(journalMode, "off"))

}

func TestDefensiveModeAppliesToEveryPhysicalConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool.db")
	db, err := sql.Open("sqlite", dbPath+"?_defensive=1")
	require.Nil(t, err)

	defer db.Close()
	db.SetMaxOpenConns(4)

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, c := range connections {
			_ = c.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		require.Nil(t, err)

		connections = append(connections, c)
	}
	for i, c := range connections {
		var before, after int64
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&before))

		_, err = c.ExecContext(ctx, fmt.Sprintf("PRAGMA schema_version=%d", before+41))
		require.Nil(t, err)

		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&after))

		require.Equal(t, before, after)

	}
}

func TestDefensiveOffPoolIsANegativeControl(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool-control.db")
	db, err := sql.Open("sqlite", dbPath+"?_defensive=0")
	require.Nil(t, err)

	defer db.Close()
	db.SetMaxOpenConns(4)

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, c := range connections {
			_ = c.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		require.Nil(t, err)

		connections = append(connections, c)
	}
	for i, c := range connections {
		var before, after int64
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&before))

		_, err = c.ExecContext(ctx, fmt.Sprintf("PRAGMA schema_version=%d", before+1))
		require.Nil(t, err)

		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&after))

		require.Equal(t, before+1, after)

	}
}

func TestDefensiveBooleanForms(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "t"} {
		t.Run("on_"+value, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "on.db")+"?_defensive="+value)
			require.Nil(t, err)

			defer db.Close()
			var before, after int64
			require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&before))

			_, err = db.Exec(fmt.Sprintf("PRAGMA schema_version=%d", before+41))
			require.Nil(t, err)

			require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&after))

			require.Equal(t, before, after)

		})
	}
}

func TestDefensiveAbsentOrFalseIsBaseline(t *testing.T) {
	for _, suffix := range []string{"", "?_defensive=0", "?_defensive=false", "?_defensive=FALSE", "?_defensive=f"} {
		t.Run(suffix, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "negative-control.db")
			db, err := sql.Open("sqlite", dbPath+suffix)
			require.Nil(t, err)

			defer db.Close()

			var before, after int64
			require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&before))

			_, err = db.Exec(fmt.Sprintf("PRAGMA schema_version=%d", before+41))
			require.Nil(t, err)

			require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&after))

			require.Equal(t, before+41, after)

		})
	}
}

// TestDefensiveRejectsJournalModeOff covers the one shorthand DSN key
// defensive mode silently neuters. SQLite turns PRAGMA journal_mode=OFF into
// a no-op that still reports success, so accepting the combination would
// honour neither parameter without saying so; applyQueryParams rejects it in
// the validation phase, before any statement runs.
func TestDefensiveRejectsJournalModeOff(t *testing.T) {
	for _, tc := range []struct {
		query   string
		wantErr string
	}{
		{"?_defensive=1&_journal_mode=OFF", "cannot take effect under _defensive"},
		{"?_defensive=1&_journal_mode=off", "cannot take effect under _defensive"},
		{"?_defensive=1&_journal=OFF", "cannot take effect under _defensive"},
		{"?_defensive=true&_journal_mode=Off", "cannot take effect under _defensive"},
		// The combination is only rejected when defensive mode would
		// actually suppress the mode change.
		{"?_defensive=0&_journal_mode=OFF", ""},
		{"?_journal_mode=OFF", ""},
		{"?_defensive=1&_journal_mode=WAL", ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "journal.db")
			db, err := sql.Open("sqlite", dbPath+tc.query)
			require.Nil(t, err)

			defer db.Close()
			err = db.Ping()
			if tc.wantErr == "" {
				require.Nil(t, err)

				return
			}
			require.False(t, err == nil || !strings.Contains(err.Error(), tc.wantErr))

			// The rejection belongs to the validation phase, so it must
			// not have run the PRAGMAs that precede it. journal_mode is
			// persistent, so a database converted here would outlive the
			// failed Open.
			var mode string
			check, err := sql.Open("sqlite", dbPath)
			require.Nil(t, err)

			defer check.Close()
			require.NoError(t, check.QueryRow("PRAGMA journal_mode").Scan(&mode))

			require.False(t, strings.EqualFold(mode, "off"))

		})
	}
}

// TestDefensiveDSNForms pins the option to the DSN shapes newConn handles
// differently. A file: DSN keeps its query string all the way into
// sqlite3_open_v2, so SQLite parses _defensive too and must ignore it; any
// other DSN has the query stripped before the open.
func TestDefensiveDSNForms(t *testing.T) {
	for _, tc := range []struct{ name, dsn string }{
		{"path", filepath.Join(t.TempDir(), "path.db") + "?_defensive=1"},
		{"file_uri", "file:" + filepath.Join(t.TempDir(), "uri.db") + "?_defensive=1"},
		{"memory", ":memory:?_defensive=1"},
		{"file_memory", "file::memory:?cache=shared&mode=memory&_defensive=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", tc.dsn)
			require.Nil(t, err)

			defer db.Close()
			db.SetMaxOpenConns(1)
			_, err = db.Exec("CREATE TABLE t(a)")
			require.Nil(t, err)

			var before, after int64
			require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&before))

			_, err = db.Exec(fmt.Sprintf("PRAGMA schema_version=%d", before+41))
			require.Nil(t, err)

			require.NoError(t, db.QueryRow("PRAGMA schema_version").Scan(&after))

			require.Equal(t, before, after)

		})
	}
}

// TestDefensiveLeavesOrdinaryUseIntact is the other half of the contract: the
// option restricts the operations documented on Driver.Open and nothing else.
// It runs alongside every other DSN parameter that could plausibly collide
// with it and exercises the features whose implementation touches the
// machinery defensive mode guards.
func TestDefensiveLeavesOrdinaryUseIntact(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "workload.db")
	db, err := sql.Open("sqlite", dbPath+"?_defensive=1&_journal_mode=WAL&_auto_vacuum=FULL"+
		"&_foreign_keys=1&_busy_timeout=1000&_synchronous=NORMAL&_txlock=immediate&_dqs=0"+
		"&_pragma=cache_size(-2000)")
	require.Nil(t, err)

	defer db.Close()
	db.SetMaxOpenConns(1)

	for _, q := range []struct{ pragma, want string }{
		{"journal_mode", "wal"},
		{"auto_vacuum", "1"},
		{"foreign_keys", "1"},
		{"busy_timeout", "1000"},
		{"synchronous", "1"},
	} {
		var got string
		require.NoError(t, db.QueryRow("PRAGMA "+q.pragma).Scan(&got))

		assert.True(t, strings.EqualFold(got, q.want))

	}

	// _dqs is applied after _defensive; it must still have taken effect.
	_, err = db.Exec(`CREATE TABLE t(a TEXT)`)
	require.Nil(t, err)

	_, err = db.Exec(`INSERT INTO t VALUES("bare-string")`)
	assert.NotNil(t, err)

	_, err = db.Exec(`
		CREATE TABLE parent(id INTEGER PRIMARY KEY);
		CREATE TABLE child(id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id));
		INSERT INTO parent VALUES(1);
	`)
	require.Nil(t, err)

	_, err = db.Exec(`INSERT INTO child VALUES(1, 99)`)
	assert.NotNil(t, err)

	// Ordinary use of a virtual table that owns shadow tables keeps working;
	// only direct writes to the shadow tables are refused.
	_, err = db.Exec(`CREATE VIRTUAL TABLE ft USING fts5(body)`)
	require.Nil(t, err)

	_, err = db.Exec(`INSERT INTO ft(body) VALUES('hello world')`)
	require.Nil(t, err)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM ft WHERE ft MATCH 'hello'`).Scan(&n))

	assert.Equal(t, 1, n)

	_, err = db.Exec(`INSERT INTO ft_data(id, block) VALUES(999, x'00')`)
	assert.NotNil(t, err)

	assert.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_dbpage`).Scan(&n))

	_, err = db.Exec(`UPDATE sqlite_dbpage SET data=zeroblob(4096) WHERE pgno=1`)
	assert.NotNil(t, err)

	_, err = db.Exec(`VACUUM`)
	assert.Nil(t, err)

	var integrity string
	require.NoError(t, db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity))

	assert.Equal(t, "ok", integrity)

}

// TestDefensiveIsPerConnection documents the scope of the option: it is a
// property of the connection, not of the database file, so a second handle
// opened without it is unrestricted. Driver.Open says so; this pins it.
func TestDefensiveIsPerConnection(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "scope.db")

	hard, err := sql.Open("sqlite", dbPath+"?_defensive=1")
	require.Nil(t, err)

	defer hard.Close()
	hard.SetMaxOpenConns(1)
	_, err = hard.ExecContext(ctx, "CREATE TABLE protected(id INTEGER PRIMARY KEY)")
	require.Nil(t, err)

	_, err = hard.ExecContext(ctx, "PRAGMA writable_schema=ON")
	require.Nil(t, err)

	_, err = hard.ExecContext(ctx, "DELETE FROM sqlite_schema WHERE name='protected'")
	require.NotNil(t, err)

	soft, err := sql.Open("sqlite", dbPath)
	require.Nil(t, err)

	defer soft.Close()
	soft.SetMaxOpenConns(1)
	_, err = soft.ExecContext(ctx, "PRAGMA writable_schema=ON")
	require.Nil(t, err)

	_, err = soft.ExecContext(ctx, "DELETE FROM sqlite_schema WHERE name='protected'")
	require.Nil(t, err)

}
