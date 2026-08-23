package sqlite

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestNullBinding(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	assert.Nil(t, err)

	_, err = db.Exec(`
	CREATE TABLE table1 (field1 varchar NULL);
	INSERT INTO table1 (field1) VALUES (?);
	`, sql.NullString{})
	assert.Nil(t, err)

	err = db.Close()
	assert.Nil(t, err)

}
