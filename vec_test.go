//go:build linux || darwin || freebsd || netbsd || openbsd || windows

package sqlite

import (
	"database/sql"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	_ "github.com/wow-look-at-my/go-sqlite/vec"
)

func TestVec(t *testing.T) {
	db, err := sql.Open(driverName, ":memory:")
	require.Nil(t, err)

	defer db.Close()

	_, err = db.Exec(`
-- https://github.com/asg017/sqlite-vec?tab=readme-ov-file#sample-usage	
create virtual table vec_examples using vec0(
  sample_embedding float[8]
);

-- vectors can be provided as JSON or in a compact binary format
insert into vec_examples(rowid, sample_embedding)
  values
    (1, '[-0.200, 0.250, 0.341, -0.211, 0.645, 0.935, -0.316, -0.924]'),
    (2, '[0.443, -0.501, 0.355, -0.771, 0.707, -0.708, -0.185, 0.362]'),
    (3, '[0.716, -0.927, 0.134, 0.052, -0.669, 0.793, -0.634, -0.162]'),
    (4, '[-0.710, 0.330, 0.656, 0.041, -0.990, 0.726, 0.385, -0.958]');
`)
	require.Nil(t, err)

	rs, err := db.Query(`
-- KNN style query
select
  rowid,
  distance
from vec_examples
where sample_embedding match '[0.890, 0.544, 0.825, 0.961, 0.358, 0.0196, 0.521, 0.175]'
order by distance
limit 2;
`)
	require.Nil(t, err)

	defer rs.Close()

	var (
		count     = 0
		rowids    = []string{"2", "1"}
		distances = []float64{2.38687372207642, 2.38978505134583}
	)
	for rs.Next() {
		var (
			rowid    string
			distance float64
		)
		require.NoError(t, rs.Scan(&rowid, &distance))

		require.Equal(t, rowids[count], rowid)

		require.LessOrEqual(t, math.Abs(distance-distances[count]), 1e-6)

		count++
	}
	require.NoError(t, rs.Err())

	require.Equal(t, len(rowids), count)

}
