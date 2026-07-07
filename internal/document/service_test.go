package document

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadDeduplicateAndDelete(t *testing.T) {
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	service := NewService(store, blobs, 1024)

	first, created, err := service.Upload(context.Background(), "user-1", "../notes.txt", []byte("第一章\n可靠的文档证据。"))
	if err != nil || !created {
		t.Fatalf("first upload = %#v, %v, %v", first, created, err)
	}
	if first.Name != "notes.txt" || first.MediaType != "text/plain" || first.Status != "queued" || !blobs.Exists(first.StorageKey) {
		t.Fatalf("unexpected document: %#v", first)
	}
	duplicate, created, err := service.Upload(context.Background(), "user-1", "renamed.txt", []byte("第一章\n可靠的文档证据。"))
	if err != nil || created || duplicate.ID != first.ID {
		t.Fatalf("duplicate upload = %#v, %v, %v", duplicate, created, err)
	}
	otherUser, created, err := service.Upload(context.Background(), "user-2", "notes.txt", []byte("第一章\n可靠的文档证据。"))
	if err != nil || !created || otherUser.ID == first.ID {
		t.Fatalf("other user upload = %#v, %v, %v", otherUser, created, err)
	}
	if err = service.Delete(context.Background(), "user-1", first.ID); err != nil {
		t.Fatal(err)
	}
	if blobs.Exists(first.StorageKey) {
		t.Fatal("deleted document blob still exists")
	}
	if _, err = service.Get(context.Background(), "user-1", first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted error = %v", err)
	}
	items, _ := service.List(context.Background(), "user-2")
	if len(items) != 1 || items[0].ID != otherUser.ID {
		t.Fatalf("user scoped list = %#v", items)
	}
}

func TestUploadRejectsUnsupportedUnsafeAndOversizeFiles(t *testing.T) {
	service := NewService(NewMemoryStore(), NewMemoryBlobStore(), 64)
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"binary.exe", []byte{0, 1, 2, 3}, ErrUnsupportedType},
		{"eicar.txt", []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE"), ErrUnsafeContent},
		{"large.txt", []byte("this document is deliberately larger than the configured sixty-four byte upload limit"), ErrValidation},
	}
	for _, test := range tests {
		if _, _, err := service.Upload(context.Background(), "user-1", test.name, test.data); !errors.Is(err, test.want) {
			t.Fatalf("upload %s error = %v, want %v", test.name, err, test.want)
		}
	}
}

func TestPDFMagicDetection(t *testing.T) {
	service := NewService(NewMemoryStore(), NewMemoryBlobStore(), 1024)
	item, created, err := service.Upload(context.Background(), "user-1", "report.bin", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF"))
	if err != nil || !created || item.MediaType != "application/pdf" {
		t.Fatalf("PDF upload = %#v, %v, %v", item, created, err)
	}
}

func TestLocalBlobStoreRejectsTraversal(t *testing.T) {
	store, err := NewLocalBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Put(context.Background(), "../escape", []byte("x")); err == nil {
		t.Fatal("traversal key was accepted")
	}
	created, err := store.Put(context.Background(), "users/u1/file", []byte("safe"))
	if err != nil || !created {
		t.Fatalf("put = %v, %v", created, err)
	}
	if _, err = os.Stat(filepath.Join(store.root, "users", "u1", "file")); err != nil {
		t.Fatal(err)
	}
}
