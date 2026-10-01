package request

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

type ArtifactInfo struct {
	RequestID   string `json:"request_id"`
	OwnerID     string `json:"owner_id"`
	WorkspaceID string `json:"workspace_id"`
	ThreadID    string `json:"thread_id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Position    int    `json:"position"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

type ArtifactPage struct {
	Items []ArtifactInfo `json:"items"`
	Total int            `json:"total"`
	Page  int            `json:"page"`
	Pages int            `json:"pages"`
}

const artifactCatalogueFrom = `FROM artifacts a JOIN requests r ON r.id=a.request_id
	WHERE a.role='output' AND r.result_expires_at>? AND (?='' OR r.owner_id=?)`

func catalogueRows(tx *sql.Tx, owner string, now int64, limit, offset int) ([]ArtifactInfo, error) {
	return storage.Rows[ArtifactInfo](tx, `SELECT a.request_id,r.owner_id,r.project_id AS workspace_id,r.thread_id,a.name,a.size,a.sha256,a.position,r.execution_completed_at AS created_at,r.result_expires_at AS expires_at
	`+artifactCatalogueFrom+` ORDER BY r.execution_completed_at DESC,r.id DESC,a.position LIMIT ? OFFSET ?`, now, owner, owner, limit, offset)
}

// CataloguePage 在同一 SQLite 快照中统计和分页，不先拉取全部产物再截断。
func CataloguePage(root, owner string, page, size int) (ArtifactPage, error) {
	if size < 1 || size > 50 {
		size = 12
	}
	result := ArtifactPage{Items: []ArtifactInfo{}}
	now := time.Now().Unix()
	err := storage.View(root, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT COUNT(*) `+artifactCatalogueFrom, now, owner, owner).Scan(&result.Total); err != nil {
			return err
		}
		result.Pages = max(1, (result.Total+size-1)/size)
		result.Page = min(max(1, page), result.Pages)
		rows, err := catalogueRows(tx, owner, now, size, (result.Page-1)*size)
		result.Items = append(result.Items, rows...)
		return err
	})
	return result, err
}

// Catalogue 直接查询产物索引，不构造执行仓库，因此查询不会触发重启恢复。
func Catalogue(root, owner string, limit int) ([]ArtifactInfo, error) {
	if limit < 1 || limit > 8000 {
		limit = 100
	}
	items := []ArtifactInfo{}
	err := storage.View(root, func(tx *sql.Tx) error {
		rows, err := catalogueRows(tx, owner, time.Now().Unix(), limit, 0)
		if err == nil {
			items = append(items, rows...)
		}
		return err
	})
	return items, err
}

func VerifyArtifacts(root, owner string) (int, error) {
	layout, err := storage.NewLayout(root)
	if err != nil {
		return 0, err
	}
	var rows []artifactRow
	err = storage.View(root, func(tx *sql.Tx) error {
		var err error
		rows, err = storage.Rows[artifactRow](tx, `SELECT a.* FROM artifacts a JOIN requests r ON r.id=a.request_id WHERE a.role='output' AND r.result_expires_at>? AND (?='' OR r.owner_id=?)`, time.Now().Unix(), owner, owner)
		return err
	})
	if err != nil {
		return 0, err
	}
	for i, a := range rows {
		if !taskIDPattern.MatchString(a.RequestID) || !filepath.IsLocal(a.Path) || filepath.Clean(a.Path) != a.Path || !strings.HasPrefix(a.Path, "output"+string(filepath.Separator)) {
			return i, fmt.Errorf("invalid artifact path")
		}
		hash, err := hashRegularFile(filepath.Join(layout.Requests(), a.RequestID, a.Path), a.Size)
		if err != nil || hash != a.SHA256 {
			return i, fmt.Errorf("产物校验失败：%s / %s", a.RequestID, a.Name)
		}
	}
	return len(rows), nil
}
