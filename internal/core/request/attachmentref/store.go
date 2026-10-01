// Package attachmentref 将微信附件协议引用拆成关系行，不持久化协议 JSON。
package attachmentref

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type media struct {
	Query string `json:"encrypt_query_param"`
	Key   string `json:"aes_key"`
	Type  int    `json:"encrypt_type"`
}
type image struct {
	URL   string `json:"url,omitempty"`
	Media *media `json:"media,omitempty"`
	Size  int    `json:"mid_size,omitempty"`
}
type file struct {
	Media  *media `json:"media,omitempty"`
	URL    string `json:"url,omitempty"`
	Name   string `json:"file_name,omitempty"`
	Length string `json:"len,omitempty"`
}
type wire struct {
	Images []*image `json:"images"`
	Files  []*file  `json:"files"`
}
type Row struct {
	Scope      string
	Parent     string
	Kind       string
	Position   int
	URL        string
	Name       string
	Length     string
	Size       int
	HasMedia   bool `json:"has_media"`
	Query      string
	Key        string
	Encryption int
}

func Save(tx *sql.Tx, scope, parent string, data []byte) error {
	if _, err := tx.Exec("DELETE FROM attachment_refs WHERE scope=? AND parent=?", scope, parent); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	if len(data) > 128<<10 {
		return fmt.Errorf("attachment references exceed limit")
	}
	var w wire
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing attachment data")
	}
	if len(w.Images) > 4 || len(w.Files) > 8 {
		return fmt.Errorf("too many attachment references")
	}
	put := func(r Row, m *media) error {
		if m != nil {
			r.HasMedia, r.Query, r.Key, r.Encryption = true, m.Query, m.Key, m.Type
		}
		return storage.Put(tx, "attachment_refs", r)
	}
	for i, v := range w.Images {
		if v == nil {
			return fmt.Errorf("nil image reference")
		}
		if err := put(Row{Scope: scope, Parent: parent, Kind: "image", Position: i, URL: v.URL, Size: v.Size}, v.Media); err != nil {
			return err
		}
	}
	for i, v := range w.Files {
		if v == nil {
			return fmt.Errorf("nil file reference")
		}
		if err := put(Row{Scope: scope, Parent: parent, Kind: "file", Position: i, URL: v.URL, Name: v.Name, Length: v.Length}, v.Media); err != nil {
			return err
		}
	}
	return nil
}

func Load(tx *sql.Tx, scope, parent string) ([]byte, error) {
	rows, err := storage.Rows[Row](tx, "SELECT * FROM attachment_refs WHERE scope=? AND parent=? ORDER BY position", scope, parent)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	w := wire{Images: []*image{}, Files: []*file{}}
	for _, r := range rows {
		var m *media
		if r.HasMedia {
			m = &media{r.Query, r.Key, r.Encryption}
		}
		switch r.Kind {
		case "image":
			w.Images = append(w.Images, &image{r.URL, m, r.Size})
		case "file":
			w.Files = append(w.Files, &file{m, r.URL, r.Name, r.Length})
		default:
			return nil, fmt.Errorf("unknown attachment kind")
		}
	}
	return json.Marshal(w)
}
