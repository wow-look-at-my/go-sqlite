// Copyright 2024 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFcntlDataVersion(t *testing.T) {
	name := filepath.Join(t.TempDir(), "tmp.db")
	db, err := sql.Open(driverName, fmt.Sprintf("file:%s", name))
	require.Nil(t, err)

	defer db.Close()

	conn, err := db.Conn(context.TODO())
	require.Nil(t, err)

	defer conn.Close()

	_, err = conn.ExecContext(context.TODO(), "create table t(v int)")
	require.Nil(t, err)

	getVersion := func() uint32 {
		t.Helper()
		var v uint32
		err = conn.Raw(func(driverConn any) error {
			fc, ok := driverConn.(FileControl)
			if !ok {
				return fmt.Errorf("driver connection didn't implement FileControl")
			}
			got, err := fc.FileControlDataVersion("main")
			v = got
			return err
		})
		require.Nil(t, err)

		return v
	}

	v0 := getVersion()

	// A commit on this connection advances the version observed here.
	_, err = conn.ExecContext(context.TODO(), "insert into t(v) values (1)")
	require.Nil(t, err)

	v1 := getVersion()
	assert.NotEqual(t, v0, v1)

	// A commit on a different connection must also advance the version.
	other, err := db.Conn(context.TODO())
	require.Nil(t, err)

	_, err = other.ExecContext(context.TODO(), "insert into t(v) values (2)")
	require.Nil(t, err)

	require.NoError(t, other.Close())

	// Force the original connection's pager to observe the change.
	_, err = conn.ExecContext(context.TODO(), "select count(*) from t")
	require.Nil(t, err)

	v := getVersion()
	assert.NotEqual(t, v1, v)

}

func TestFcntlPersistWAL(t *testing.T) {
	t.Run("WAL is cleaned up without persist WAL", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "tmp.db")
		walName := name + "-wal"
		db, err := sql.Open(driverName, fmt.Sprintf("file:%s", name))
		require.Nil(t, err)

		defer db.Close()

		// enable WAL journal
		_, err = db.Exec("pragma journal_mode = WAL")
		require.Nil(t, err)

		_, err = db.Exec("create table t(b int)")
		require.Nil(t, err)

		// database file must exist after creating a table
		_, err = os.Stat(name)
		require.Nil(t, err)

		// wal file must exist after creating a table
		_, err = os.Stat(walName)
		require.Nil(t, err)

		require.NoError(t, db.Close())

		// database file must exist after closing it
		_, err = os.Stat(name)
		require.Nil(t, err)

		// wal file must NOT exist after closing the db
		if _, err := os.Stat(walName); err == nil {
			t.Errorf("expected WAL file %s to not exist after closing db", walName)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	})

	t.Run("WAL is not cleaned up with persist WAL", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "tmp.db")
		walName := name + "-wal"
		db, err := sql.Open(driverName, fmt.Sprintf("file:%s", name))
		require.Nil(t, err)

		defer db.Close()

		conn, err := db.Conn(context.TODO())
		require.Nil(t, err)

		// enable persist WAL for a connection, normally this is done with a hook
		err = conn.Raw(func(driverConn any) error {
			fc, ok := driverConn.(FileControl)
			if !ok {
				return fmt.Errorf("driver connection didn't implement FileControl")
			}

			// query
			mode, err := fc.FileControlPersistWAL("main", -1)
			if err != nil {
				return fmt.Errorf("file control call failed: %w", err)
			} else if mode != 0 {
				return fmt.Errorf("file control call returned unexpected mode: %d", mode)
			}

			// enable
			mode, err = fc.FileControlPersistWAL("main", 1)
			if err != nil {
				return fmt.Errorf("file control call failed: %w", err)
			} else if mode != 1 {
				return fmt.Errorf("file control call returned unexpected mode: %d", mode)
			}

			// verify
			mode, err = fc.FileControlPersistWAL("main", -1)
			if err != nil {
				return fmt.Errorf("file control call failed: %w", err)
			} else if mode != 1 {
				return fmt.Errorf("file control call returned unexpected mode: %d", mode)
			}

			return nil
		})
		require.Nil(t, err)

		_, err = conn.ExecContext(context.TODO(), "pragma journal_mode = WAL")
		require.Nil(t, err)

		_, err = conn.ExecContext(context.TODO(), "create table t(b int)")
		require.Nil(t, err)

		// database file must exist after creating a table
		_, err = os.Stat(name)
		require.Nil(t, err)

		// wal file must exist after creating a table
		_, err = os.Stat(walName)
		require.Nil(t, err)

		// close connection, should persist WAL
		require.NoError(t, conn.Close())

		// close database, should persist WAL
		require.NoError(t, db.Close())

		// database file must exist after closing it
		_, err = os.Stat(name)
		require.Nil(t, err)

		// wal file must exist after closing the db
		_, err = os.Stat(walName)
		assert.Nil(t, err)

	})
}
