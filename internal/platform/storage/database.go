package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const SchemaVersion = 1

// Open 的连接由调用方关闭。短事务独占自己的连接，不共享事务外查询。
func Open(root string) (*sql.DB, error) {
	layout, err := NewLayout(root)
	if err != nil {
		return nil, err
	}
	if err = layout.Ensure(); err != nil {
		return nil, err
	}
	path := layout.Database()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err == nil && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("database must be a regular file")
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if err := os.Chmod(path+suffix, 0o600); err != nil && !(suffix != "" && os.IsNotExist(err)) {
				return nil, err
			}
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_txlock", "immediate")
	for _, pragma := range []string{"busy_timeout(5000)", "foreign_keys(1)", "synchronous(FULL)", "secure_delete(1)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) { db.Close(); return nil, err }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != SchemaVersion {
		return fail(fmt.Errorf("unsupported SQLite schema %d", version))
	}
	if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fail(err)
	}
	if version == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fail(err)
		}
		if _, err = tx.ExecContext(ctx, schema); err == nil {
			_, err = tx.ExecContext(ctx, "PRAGMA user_version=1")
		}
		if err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

func Update(root string, fn func(*sql.Tx) error) error {
	return transaction(root, false, fn)
}

func transaction(root string, readOnly bool, fn func(*sql.Tx) error) error {
	db, err := Open(root)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func View(root string, fn func(*sql.Tx) error) error { return transaction(root, true, fn) }

// Backup 使用 SQLite 自身生成一致快照，不复制可能缺少 WAL 的主文件。
func Backup(root, destination string) error {
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("backup destination must not exist")
	}
	db, err := Open(root)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.Exec("VACUUM INTO ?", destination); err != nil {
		return err
	}
	return os.Chmod(destination, 0o600)
}
