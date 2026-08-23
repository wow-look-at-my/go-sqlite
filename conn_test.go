// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

import (
	"database/sql"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestColumnTextScan exercises the rows.Scan TEXT-column path that
// (*conn).columnText feeds. The test covers the three branches of that
// function: NULL (returned as empty string), empty string, and non-empty
// values of varying lengths. A regression on the unsafe.String pattern would
// either truncate the result, expose Go-heap garbage, or trip the race
// detector under -race.
func TestColumnTextScan(t *testing.T) {
	db, err := sql.Open(driverName, "file::memory:")
	require.Nil(t, err)

	defer db.Close()

	_, err = db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, s TEXT)`)
	require.Nil(t, err)

	long := strings.Repeat("a1B2c3D4", 256) // 2048 bytes, well past inline storage
	cases := []struct {
		id int64
		s  string
	}{
		{1, "hello"},
		{2, ""},
		{3, "unicode é 中文 \U0001F600"},
		{4, long},
	}
	for _, c := range cases {
		_, err = db.Exec(`INSERT INTO t(id, s) VALUES (?, ?)`, c.id, c.s)
		require.Nil(t, err)

	}

	for _, c := range cases {
		var got string
		require.NoError(t, db.QueryRow(`SELECT s FROM t WHERE id = ?`, c.id).Scan(&got))

		assert.Equal(t, c.s, got)

	}

	// Read all rows in a single query so columnText is invoked many times in
	// quick succession; a stale-pointer regression would surface here as the
	// last row's text bleeding into earlier rows.
	rows, err := db.Query(`SELECT s FROM t ORDER BY id`)
	require.Nil(t, err)

	defer rows.Close()

	var got []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))

		got = append(got, s)
	}
	require.NoError(t, rows.Err())

	want := []string{"hello", "", "unicode é 中文 \U0001F600", long}
	require.Equal(t, len(want), len(got))

	for i := range got {
		assert.Equal(t, want[i], got[i])

	}
}

// benchColumnTextScan exercises the rows.Scan TEXT path many times so the
// per-row cost is dominated by (*conn).columnText, not by statement
// preparation or driver bookkeeping. textLen controls the per-row payload
// size; the default mode in conn.go did one make+one string(b)-copy per
// invocation, which means at small sizes the alloc count dominates and at
// large sizes the memcpy dominates.
func benchColumnTextScan(b *testing.B, textLen int) {
	db, err := sql.Open(driverName, "file::memory:")
	require.Nil(b, err)

	defer db.Close()

	_, err = db.Exec(`CREATE TABLE t (s TEXT)`)
	require.Nil(b, err)

	payload := strings.Repeat("X", textLen)
	const rows = 1000
	tx, err := db.Begin()
	require.Nil(b, err)

	stmt, err := tx.Prepare(`INSERT INTO t (s) VALUES (?)`)
	require.Nil(b, err)

	for i := 0; i < rows; i++ {
		_, err = stmt.Exec(payload)
		require.Nil(b, err)

	}
	stmt.Close()
	require.NoError(b, tx.Commit())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := db.Query(`SELECT s FROM t`)
		require.Nil(b, err)

		for r.Next() {
			var s string
			require.NoError(b, r.Scan(&s))

		}
		require.NoError(b, r.Err())

		r.Close()
	}
}

// BenchmarkColumnTextScanShort measures the rows.Scan TEXT path on a small
// payload where alloc count dominates the cost.
func BenchmarkColumnTextScanShort(b *testing.B) {
	benchColumnTextScan(b, 16)
}

// BenchmarkColumnTextScanMedium measures the rows.Scan TEXT path on a
// medium payload where both alloc count and memcpy contribute.
func BenchmarkColumnTextScanMedium(b *testing.B) {
	benchColumnTextScan(b, 256)
}

// BenchmarkColumnTextScanLong measures the rows.Scan TEXT path on a large
// payload where the second memcpy that the old string(b) conversion forced
// is the dominant cost.
func BenchmarkColumnTextScanLong(b *testing.B) {
	benchColumnTextScan(b, 4096)
}

// TestColumnTypeDatabaseTypeNameCache verifies that the rows decltype cache
// returns the same uppercase declared type for every row of a result set
// (across multiple Next calls) and matches the schema's declared type case-
// insensitively. The result is compared against a Next loop that reads the
// decltype on each row, so a stale or per-row regression would surface as a
// type-name mismatch between the two readers.
func TestColumnTypeDatabaseTypeNameCache(t *testing.T) {
	db, err := sql.Open(driverName, "file::memory:")
	require.Nil(t, err)

	defer db.Close()

	// Mix declared types in different cases and across all SQLite storage
	// classes. The varied casing exercises the strings.ToUpper path of the
	// cache.
	_, err = db.Exec(`CREATE TABLE t (a integer, b TEXT, c BlOb, d DATETIME, e Date, f boolean)`)
	require.Nil(t, err)

	_, err = db.Exec(`INSERT INTO t VALUES (1, 'x', X'00', '2025-01-15 10:30:00', '2025-01-15', 1)`)
	require.Nil(t, err)

	_, err = db.Exec(`INSERT INTO t VALUES (2, 'y', X'01', '2025-01-16 11:00:00', '2025-01-16', 0)`)
	require.Nil(t, err)

	rows, err := db.Query(`SELECT a, b, c, d, e, f FROM t`)
	require.Nil(t, err)

	defer rows.Close()

	types, err := rows.ColumnTypes()
	require.Nil(t, err)

	wantTypes := []string{"INTEGER", "TEXT", "BLOB", "DATETIME", "DATE", "BOOLEAN"}
	require.Equal(t, len(wantTypes), len(types))

	for i, ct := range types {
		got := ct.DatabaseTypeName()
		assert.Equal(t, wantTypes[i], got)

	}

	// Drain rows; reading the cache for every row of a multi-row scan must
	// keep returning the same values for the lifetime of the result set.
	rowCount := 0
	for rows.Next() {
		rowCount++
		for i, ct := range types {
			got := ct.DatabaseTypeName()
			assert.Equal(t, wantTypes[i], got)

		}
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, 2, rowCount)

}

// benchTextToTimeScan exercises the rows.Next + Scan path under
// _texttotime=1, which fires r.ColumnTypeDatabaseTypeName(i) for every TEXT
// column on every row to decide whether to parse the value as time. Before
// the decltype cache landed, each call did a libc.GoString + strings.ToUpper
// per row per column. With the cache the lookup is a single slice index.
func benchTextToTimeScan(b *testing.B, columnCount int) {
	db, err := sql.Open(driverName, "file::memory:?_texttotime=1")
	require.Nil(b, err)

	defer db.Close()

	var schemaCols, selectCols, insertCols, insertVals []string
	for i := 0; i < columnCount; i++ {
		schemaCols = append(schemaCols, fmt.Sprintf("c%d DATETIME", i))
		selectCols = append(selectCols, fmt.Sprintf("c%d", i))
		insertCols = append(insertCols, fmt.Sprintf("c%d", i))
		insertVals = append(insertVals, "?")
	}
	_, err = db.Exec(`CREATE TABLE t (` + strings.Join(schemaCols, ", ") + `)`)
	require.Nil(b, err)

	const rows = 1000
	tx, err := db.Begin()
	require.Nil(b, err)

	stmt, err := tx.Prepare(`INSERT INTO t (` + strings.Join(insertCols, ", ") + `) VALUES (` + strings.Join(insertVals, ", ") + `)`)
	require.Nil(b, err)

	args := make([]any, columnCount)
	for i := 0; i < columnCount; i++ {
		args[i] = "2025-01-15 10:30:00"
	}
	for i := 0; i < rows; i++ {
		_, err = stmt.Exec(args)
		require.Nil(b, err)

	}
	stmt.Close()
	require.NoError(b, tx.Commit())

	query := `SELECT ` + strings.Join(selectCols, ", ") + ` FROM t`
	dest := make([]any, columnCount)
	destPtrs := make([]any, columnCount)
	for i := range dest {
		destPtrs[i] = &dest[i]
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := db.Query(query)
		require.Nil(b, err)

		for r.Next() {
			require.NoError(b, r.Scan(destPtrs))

		}
		require.NoError(b, r.Err())

		r.Close()
	}
}

// BenchmarkTextToTimeScan1Col measures the _texttotime=1 hot path with a
// single DATETIME column, isolating the per-row decltype lookup cost.
func BenchmarkTextToTimeScan1Col(b *testing.B) {
	benchTextToTimeScan(b, 1)
}

// BenchmarkTextToTimeScan5Cols measures the same path with a wider result
// set so the cache savings scale linearly with column count.
func BenchmarkTextToTimeScan5Cols(b *testing.B) {
	benchTextToTimeScan(b, 5)
}

// TestColumnTypeScanTypeDecltypeCache locks down the ColumnTypeScanType comparison logic
// that was rewritten from a lowercase decltype switch to the cached uppercase
// switch. Each of the four arms that look at the cache (INTEGER -> BOOLEAN,
// INTEGER -> DATE/DATETIME/TIME/TIMESTAMP, TEXT default, TEXT under
// _texttotime -> DATE/DATETIME/TIME/TIMESTAMP) gets a column with a
// mixed-case declared type so a regression on the case-folding path would
// surface as a wrong reflect.Type for that column.
func TestColumnTypeScanTypeDecltypeCache(t *testing.T) {
	// Each case inserts one row before querying so ColumnTypeScanType sees
	// the actual storage class (sqlite3_column_type), not SQLITE_NULL.
	cases := []struct {
		dsn   string
		col   string
		value any
		want  reflect.Type
		label string
	}{
		// INTEGER + BOOLEAN decltype (case-insensitive) always returns bool.
		{"file::memory:", "b BoOlEaN", int64(1), reflect.TypeOf(false), "BOOLEAN INTEGER default"},
		{"file::memory:?_texttotime=1", "b boolean", int64(0), reflect.TypeOf(false), "BOOLEAN INTEGER + _texttotime"},

		// INTEGER + DATE/DATETIME/TIME/TIMESTAMP decltype always returns
		// time.Time independent of any DSN flag (the cache-driven switch
		// has no flag gate on the INTEGER arm).
		{"file::memory:", "d Date", int64(1736899200), reflect.TypeOf(time.Time{}), "DATE INTEGER default"},
		{"file::memory:", "dt datetime", int64(1736941800), reflect.TypeOf(time.Time{}), "DATETIME INTEGER default"},
		{"file::memory:", "tm TIME", int64(37800), reflect.TypeOf(time.Time{}), "TIME INTEGER default"},
		{"file::memory:", "ts TimeStamp", int64(1736941800), reflect.TypeOf(time.Time{}), "TIMESTAMP INTEGER default"},

		// INTEGER without a recognised decltype falls back to int64.
		{"file::memory:", "n integer", int64(42), reflect.TypeOf(int64(0)), "plain INTEGER default"},
		{"file::memory:", "x BIGINT", int64(42), reflect.TypeOf(int64(0)), "unrecognised INTEGER decltype"},

		// TEXT default returns string, even for DATETIME-shaped decltypes,
		// because the textToTime opt-in is off.
		{"file::memory:", "s TEXT", "hello", reflect.TypeOf(""), "plain TEXT default"},
		{"file::memory:", "dt DateTime", "2025-01-15 10:30:00", reflect.TypeOf(""), "DATETIME TEXT default (no textToTime)"},

		// TEXT under _texttotime=1 returns time.Time for the four recognised
		// decltypes (mixed case), and string for everything else.
		{"file::memory:?_texttotime=1", "d Date", "2025-01-15", reflect.TypeOf(time.Time{}), "DATE TEXT + _texttotime"},
		{"file::memory:?_texttotime=1", "dt DateTime", "2025-01-15 10:30:00", reflect.TypeOf(time.Time{}), "DATETIME TEXT + _texttotime"},
		{"file::memory:?_texttotime=1", "tm time", "10:30:00", reflect.TypeOf(time.Time{}), "TIME TEXT + _texttotime"},
		{"file::memory:?_texttotime=1", "ts TIMESTAMP", "2025-01-15 10:30:00", reflect.TypeOf(time.Time{}), "TIMESTAMP TEXT + _texttotime"},
		{"file::memory:?_texttotime=1", "s TEXT", "hello", reflect.TypeOf(""), "plain TEXT + _texttotime"},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			db, err := sql.Open(driverName, c.dsn)
			require.Nil(t, err)

			defer db.Close()

			_, err = db.Exec(`CREATE TABLE t (` + c.col + `)`)
			require.Nil(t, err)

			_, err = db.Exec(`INSERT INTO t VALUES (?)`, c.value)
			require.Nil(t, err)

			rows, err := db.Query(`SELECT * FROM t`)
			require.Nil(t, err)

			defer rows.Close()
			require.True(t, rows.Next())

			types, err := rows.ColumnTypes()
			require.Nil(t, err)

			require.Equal(t, 1, len(types))

			got := types[0].ScanType()
			assert.Equal(t, c.want, got)

		})
	}
}

// TestParseTimeFormatCache verifies that the per-rows-per-column format-index
// hint reused by (*conn).parseTime keeps returning correct parsed time values
// across many rows of a steady-format column, and that a column whose format
// switches mid-result-set still parses correctly via the fallthrough path.
func TestParseTimeFormatCache(t *testing.T) {
	db, err := sql.Open(driverName, "file::memory:")
	require.Nil(t, err)

	defer db.Close()

	_, err = db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, dt DATETIME)`)
	require.Nil(t, err)

	// First three rows use the same canonical SQLite TEXT format (matches
	// format index 2 of parseTimeFormats: "2006-01-02 15:04:05.999999999").
	// Row 4 uses the ISO-T format (matches index 3). Row 5 uses the
	// date-only fallback (index 6). After the cache stabilises on row 1, the
	// hinted format helps rows 2 and 3 directly and rows 4-5 fall through.
	values := []string{
		"2025-01-15 10:30:00",
		"2025-01-15 11:00:00",
		"2025-01-15 11:30:00",
		"2025-01-16T08:15:00",
		"2025-01-17",
	}
	for i, v := range values {
		_, err = db.Exec(`INSERT INTO t(id, dt) VALUES (?, ?)`, i+1, v)
		require.Nil(t, err)

	}

	rows, err := db.Query(`SELECT dt FROM t ORDER BY id`)
	require.Nil(t, err)

	defer rows.Close()

	wantTimes := []time.Time{
		time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 15, 11, 30, 0, 0, time.UTC),
		time.Date(2025, 1, 16, 8, 15, 0, 0, time.UTC),
		time.Date(2025, 1, 17, 0, 0, 0, 0, time.UTC),
	}
	i := 0
	for rows.Next() {
		require.Less(t, i, len(wantTimes))

		var got time.Time
		require.NoError(t, rows.Scan(&got))

		assert.True(t, got.Equal(wantTimes[i]))

		i++
	}
	require.NoError(t, rows.Err())

	require.Equal(t, len(wantTimes), i)

}

// benchParseTimeScan exercises the rows.Next + Scan path on a DATETIME TEXT
// column. With the parseTime format-index cache, every row after the first
// hits the hinted format directly; without the cache, every row re-walks
// the parseTimeFormats list until it finds a match.
func benchParseTimeScan(b *testing.B) {
	db, err := sql.Open(driverName, "file::memory:")
	require.Nil(b, err)

	defer db.Close()

	_, err = db.Exec(`CREATE TABLE t (dt DATETIME)`)
	require.Nil(b, err)

	const rows = 1000
	tx, err := db.Begin()
	require.Nil(b, err)

	stmt, err := tx.Prepare(`INSERT INTO t (dt) VALUES (?)`)
	require.Nil(b, err)

	for i := 0; i < rows; i++ {
		// Canonical SQLite TEXT datetime format (index 2 of
		// parseTimeFormats).
		_, err = stmt.Exec("2025-01-15 10:30:00")
		require.Nil(b, err)

	}
	stmt.Close()
	require.NoError(b, tx.Commit())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := db.Query(`SELECT dt FROM t`)
		require.Nil(b, err)

		var got time.Time
		for r.Next() {
			require.NoError(b, r.Scan(&got))

		}
		require.NoError(b, r.Err())

		r.Close()
	}
}

// BenchmarkParseTimeScan measures the rows.Next DATETIME TEXT path with the
// format-index cache active.
func BenchmarkParseTimeScan(b *testing.B) {
	benchParseTimeScan(b)
}
