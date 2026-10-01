package storage

import (
	"database/sql"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var primaryKeys = map[string]string{
	"settings": "key", "workspaces": "id", "codex_env": "key", "voice_providers": "id", "credentials": "ilink_bot_id",
	"sync_cursors": "bot_id", "sync_receipts": "bot_id,source", "preferences": "owner_id", "remote_locks": "owner_id",
	"target_owners": "owner_id", "target_intents": "owner_id,id", "target_selections": "owner_id,workspace_id",
	"notices": "id", "menu_receipts": "owner_id,source", "drafts": "owner_id", "draft_receipts": "source",
	"requests": "id", "rejected_sources": "source", "cleared_sources": "source", "request_inputs": "request_id",
	"attachment_refs": "scope,parent,kind,position", "artifacts": "request_id,role,position", "completions": "request_id",
	"results": "request_id", "result_urls": "request_id,position", "delivery_receipts": "request_id,position",
	"import_files": "path", "import_sources": "name", "snapshot_entries": "path", "deployment_receipts": "id",
}

// 仅映射平面行；关系与事务由领域仓库显式处理，不序列化对象或 JSON 文档。
type column struct {
	name  string
	index []int
}

func fields(t reflect.Type) ([]column, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("SQL row must be a struct")
	}
	var names []column
	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if !identifier.MatchString(name) {
			return nil, fmt.Errorf("invalid SQL column %q", name)
		}
		switch f.Type.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint32, reflect.Float64:
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.Uint8 {
				return nil, fmt.Errorf("non-scalar SQL field %s", name)
			}
		default:
			return nil, fmt.Errorf("non-scalar SQL field %s", name)
		}
		names = append(names, column{name, f.Index})
	}
	return names, nil
}

func Put(tx *sql.Tx, table string, row any) error {
	key, known := primaryKeys[table]
	if !identifier.MatchString(table) || !known {
		return fmt.Errorf("invalid SQL table")
	}
	v := reflect.ValueOf(row)
	names, err := fields(v.Type())
	if err != nil {
		return err
	}
	columns, placeholders, updates := make([]string, len(names)), make([]string, len(names)), make([]string, len(names))
	args := make([]any, len(names))
	for i, field := range names {
		name := field.name
		columns[i], placeholders[i], updates[i] = `"`+name+`"`, "?", `"`+name+`"=excluded."`+name+`"`
		args[i] = v.FieldByIndex(field.index).Interface()
	}
	_, err = tx.Exec(`INSERT INTO "`+table+`" (`+strings.Join(columns, ",")+`) VALUES (`+strings.Join(placeholders, ",")+`) ON CONFLICT (`+key+`) DO UPDATE SET `+strings.Join(updates, ","), args...)
	return err
}

func Rows[T any](tx *sql.Tx, query string, args ...any) ([]T, error) {
	var zero T
	names, err := fields(reflect.TypeOf(zero))
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	indexes := make([]int, len(columns))
	for i, col := range columns {
		indexes[i] = -1
		for j, field := range names {
			if col == field.name {
				indexes[i] = j
				break
			}
		}
		if indexes[i] < 0 {
			return nil, fmt.Errorf("unmapped SQL column %s", col)
		}
	}
	var result []T
	for rows.Next() {
		var item T
		v := reflect.ValueOf(&item).Elem()
		targets := make([]any, len(columns))
		for i, index := range indexes {
			targets[i] = v.FieldByIndex(names[index].index).Addr().Interface()
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
