package request

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// 清单读取、单文件下载和完整交付有不同的校验边界，不能互相替代。
func TestReadMetadataAndDownloadOnlySelectedArtifact(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(t.TempDir())
	check(err)
	input := testStartInput("reading", "owner", "project")
	input.Files = []InputAttachment{{Name: "input.txt", ContentType: "text/plain", Data: []byte("input")}}
	task := mustStart(t, store, input)
	loaded, err := store.LoadRequest("owner", task.ID)
	check(err)
	check(os.Remove(loaded.Files[0].AbsolutePath))
	metadata, err := store.InspectRequest(" owner ", " "+task.ID+" ")
	check(err)
	if metadata.Text != input.Text || len(metadata.Files) != 1 || metadata.Files[0].Name != "input.txt" {
		t.Fatalf("input metadata missing: %+v", metadata)
	}
	if _, err := store.LoadRequest("owner", task.ID); err == nil {
		t.Fatal("execution accepted missing input")
	}
	if _, err := store.InspectRequest("foreign", task.ID); err == nil {
		t.Fatal("foreign input metadata exposed")
	}
	outbox, err := store.PrepareOutbox("owner", task.ID)
	check(err)
	first, second := filepath.Join(outbox, "first.txt"), filepath.Join(outbox, "second.txt")
	check(os.WriteFile(first, []byte("first"), 0600))
	check(os.WriteFile(second, []byte("second"), 0600))
	_, err = store.FreezeResult("owner", task.ID, FreezeResultInput{Reply: "retained text", ArtifactPaths: []string{first, second}})
	check(err)
	_, err = store.Finish("owner", task.ID, StateSucceeded, "")
	check(err)
	check(os.Remove(second))
	result, err := store.InspectResult("owner", task.ID)
	check(err)
	if result.Reply != "retained text" || len(result.Artifacts) != 2 {
		t.Fatalf("result metadata missing: %+v", result)
	}
	if _, err := store.InspectResult("foreign", task.ID); err == nil {
		t.Fatal("foreign result metadata exposed")
	}
	if _, err := store.LoadResult("owner", task.ID); err == nil {
		t.Fatal("full result verification accepted missing file")
	}
	if _, _, err := store.BeginRedelivery("owner", task.ID, uuid.NewString()); err == nil {
		t.Fatal("redelivery accepted missing file")
	}
	result, err = store.InspectResult("owner", task.ID)
	check(err)
	if len(result.Attempts) != 0 || result.Receipt.Outcome != DeliveryPending {
		t.Fatal("failed validation registered a delivery")
	}
	file, artifact, err := store.OpenArtifact(" owner ", " "+task.ID+" ", 0)
	check(err)
	data, err := io.ReadAll(file)
	check(err)
	check(file.Close())
	if string(data) != "first" || artifact.Name != "first.txt" {
		t.Fatalf("download not rewound or wrong artifact: %q %+v", data, artifact)
	}
	for _, index := range []int{-1, 1, 2} {
		if file, _, err := store.OpenArtifact("owner", task.ID, index); err == nil {
			file.Close()
			t.Fatalf("unavailable artifact %d opened", index)
		}
	}
	rejectDownload := func() {
		t.Helper()
		if file, _, err := store.OpenArtifact("owner", task.ID, 0); err == nil {
			file.Close()
			t.Fatal("changed or symbolic artifact opened")
		}
	}
	check(os.WriteFile(first, []byte("wrong"), 0600))
	rejectDownload()
	check(os.Remove(first))
	outside := filepath.Join(t.TempDir(), "first.txt")
	check(os.WriteFile(outside, []byte("first"), 0600))
	check(os.Symlink(outside, first))
	rejectDownload()
	check(os.Remove(first))
	check(os.WriteFile(first, []byte("first"), 0600))
	moved := filepath.Join(t.TempDir(), "output")
	check(os.Rename(outbox, moved))
	check(os.Symlink(moved, outbox))
	rejectDownload()
}
