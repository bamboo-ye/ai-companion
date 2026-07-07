package ledger

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestQueueExportIsIdempotentAndRejectsKeyReuse(t *testing.T) {
	service := NewService(NewMemoryStore())
	first, created, err := service.QueueExport(context.Background(), "u1", "monthly-2026-07", "2026-07", "Asia/Shanghai", "CNY")
	if err != nil || !created {
		t.Fatalf("first export = %#v created=%v err=%v", first, created, err)
	}
	duplicate, created, err := service.QueueExport(context.Background(), "u1", "monthly-2026-07", "2026-07", "Asia/Shanghai", "CNY")
	if err != nil || created || duplicate.ID != first.ID {
		t.Fatalf("duplicate export = %#v created=%v err=%v", duplicate, created, err)
	}
	if _, _, err = service.QueueExport(context.Background(), "u1", "monthly-2026-07", "2026-06", "Asia/Shanghai", "CNY"); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("reused key error = %v", err)
	}
}

type captureExporter struct{ payload ExportPayload }

func (e *captureExporter) Export(_ context.Context, payload ExportPayload) ([]byte, error) {
	e.payload = payload
	return []byte("PK-test-workbook"), nil
}

func TestServiceExportUsesMonthlyFilteredEntries(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	service.now = func() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }
	exporter := &captureExporter{}
	service.SetExporter(exporter)
	candidate, _ := service.ParseCandidate(ctx, "u1", "", "昨天打车 36 元", "Asia/Shanghai")
	if _, _, err := service.Confirm(ctx, "u1", candidate.ID, "export-entry", ""); err != nil {
		t.Fatal(err)
	}
	data, filename, err := service.Export(ctx, "u1", "2026-07", "Asia/Shanghai", "CNY")
	if err != nil || filename != "ledger-2026-07-CNY.xlsx" || string(data) != "PK-test-workbook" {
		t.Fatalf("export = %q %q %v", data, filename, err)
	}
	if len(exporter.payload.Entries) != 1 || exporter.payload.Entries[0].AmountMinor != 3600 {
		t.Fatalf("payload = %#v", exporter.payload)
	}
}

func TestArtifactToolExporterIntegration(t *testing.T) {
	node := os.Getenv("AI_COMPANION_TEST_NODE")
	script := os.Getenv("AI_COMPANION_TEST_SPREADSHEET_WORKER")
	if node == "" || script == "" {
		t.Skip("artifact-tool integration environment is not configured")
	}
	data, err := (ArtifactToolExporter{Executable: node, ScriptPath: script, Timeout: 30 * time.Second}).Export(context.Background(), ExportPayload{
		Month: "2026-07", Currency: "CNY", Timezone: "Asia/Shanghai", GeneratedAt: time.Now().UTC(),
		Entries: []Entry{{ID: "entry-1", Direction: "expense", Currency: "CNY", AmountMinor: 3600, Category: "transport", Merchant: "出租车", OccurredAt: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC), Timezone: "Asia/Shanghai", Status: "active"}},
	})
	if err != nil || len(data) < 1000 || string(data[:2]) != "PK" {
		t.Fatalf("artifact export bytes=%d err=%v", len(data), err)
	}
}
