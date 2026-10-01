package storage

import (
	"database/sql"
	"errors"
)

type Setting struct {
	Key   string
	Value string
}

func GetSetting(root, key string) (value string, found bool, err error) {
	err = View(root, func(tx *sql.Tx) error {
		err := tx.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	return
}

func SetSetting(root, key, value string) error {
	return Update(root, func(tx *sql.Tx) error { return Put(tx, "settings", Setting{key, value}) })
}
